package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/jackc/pgx/v5/pgconn"
)

func gatewayFixture(t *testing.T, versioned bool) (*Store, NativeGatewayTicketRequest) {
	t.Helper()
	db, a, _ := nativeAccountFixture(t, "linux")
	policy, err := db.EnsureDesktopAccessPolicy(t.Context(), a.DesktopVMID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkDesktopAccessPolicyApplied(t.Context(), a.DesktopVMID, policy.DesiredRevision, "linux"); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("unique initiating Native device fixture"))
	if err := db.CreateNativeSession(t.Context(), digest[:], a.UserID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	connection := DesktopConnectionSession{ID: "conn_gateway_test", UserID: a.UserID, DesktopVMID: a.DesktopVMID, GuestUsername: a.GuestUsername, ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Second)}
	if versioned {
		bound, err := db.BindNativeGuestAccount(t.Context(), a, digest[:])
		if err != nil {
			t.Fatal("bind Native account", err)
		}
		pending, err := db.BeginNativeGuestCredential(t.Context(), bound, connection.ID, connection.ExpiresAt, digest[:])
		if err != nil {
			t.Fatal("reserve Native credential", err)
		}
		if _, err = db.CompleteNativeGuestConnection(t.Context(), pending); err != nil {
			t.Fatal("complete Native connection", err)
		}
	} else if _, err := db.CreateNativeDesktopConnectionSession(t.Context(), connection, digest[:]); err != nil {
		t.Fatal(err)
	}
	var origin []byte
	if err := db.pool.QueryRow(t.Context(), `SELECT native_session_digest FROM desktop_connection_sessions WHERE id=$1`, connection.ID).Scan(&origin); err != nil || !bytes.Equal(origin, digest[:]) {
		t.Fatal("connection lost exact initiating device", err)
	}
	cert := sha256.Sum256([]byte("test RDP certificate DER"))
	return db, NativeGatewayTicketRequest{ConnectionID: connection.ID, NativeDigest: digest[:], GatewayID: "gw_testprimary", Target: netip.MustParseAddr("10.31.0.110"), CertificateSHA256: cert[:], PolicyRevision: policy.DesiredRevision}
}

func TestNativeGatewayTicketLifecycleAndImmutableAuthority(t *testing.T) {
	for _, versioned := range []bool{false, true} {
		t.Run(map[bool]string{false: "native_legacy_path", true: "versioned_native"}[versioned], func(t *testing.T) {
			db, req := gatewayFixture(t, versioned)
			second := sha256.Sum256([]byte("another valid device of the same user"))
			if err := db.CreateNativeSession(t.Context(), second[:], "native-a", time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			wrong := req
			wrong.NativeDigest = second[:]
			if _, err := db.CreateNativeGatewayTicket(t.Context(), wrong); !errors.Is(err, ErrNotFound) {
				t.Fatal("another device adopted connection", err)
			}
			ticket, err := db.CreateNativeGatewayTicket(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			var expiry time.Time
			var ttl float64
			if err = db.pool.QueryRow(t.Context(), `SELECT expires_at,extract(epoch FROM (expires_at-created_at))::float8 FROM gateway_session_tickets`).Scan(&expiry, &ttl); err != nil || ttl <= 0 || ttl > 30 || !expiry.Equal(ticket.ExpiresAt) {
				t.Fatal("database ticket TTL is not bounded or expiry changed in transit", err)
			}
			encoded, _ := json.Marshal(ticket)
			if bytes.Contains(encoded, []byte(ticket.Token)) {
				t.Fatal("ticket secret serialized implicitly")
			}
			var persisted string
			if err = db.pool.QueryRow(t.Context(), `SELECT row_to_json(g)::text FROM gateway_session_tickets g`).Scan(&persisted); err != nil || strings.Contains(persisted, ticket.Token) {
				t.Fatal("plaintext ticket persisted", err)
			}
			if _, err = db.CreateNativeGatewayTicket(t.Context(), req); !errors.Is(err, ErrConflict) {
				t.Fatal("logical connection received another ticket", err)
			}
			if _, err = db.RedeemNativeGatewayTicket(t.Context(), "gw_othernode", ticket.Token); !errors.Is(err, ErrNotFound) {
				t.Fatal("wrong Gateway consumed ticket", err)
			}
			grant, err := db.RedeemNativeGatewayTicket(t.Context(), req.GatewayID, ticket.Token)
			if err != nil {
				t.Fatal(err)
			}
			if grant.ConnectionID != req.ConnectionID || grant.GatewayID != req.GatewayID || grant.Target != req.Target || !bytes.Equal(grant.CertificateSHA256, req.CertificateSHA256) || grant.ValidFor <= 0 || grant.ValidFor > 5*time.Second {
				t.Fatal("grant lost pinned authority")
			}
			if _, err = db.RedeemNativeGatewayTicket(t.Context(), req.GatewayID, ticket.Token); !errors.Is(err, ErrNotFound) {
				t.Fatal("spent ticket replayed", err)
			}
			if _, err = db.RenewNativeGatewayGrant(t.Context(), "gw_othernode", grant.TunnelID); !errors.Is(err, ErrNotFound) {
				t.Fatal("cross-Gateway lease renewal", err)
			}
			if err = db.CloseNativeGatewayGrant(t.Context(), "gw_othernode", grant.TunnelID); err != nil {
				t.Fatal(err)
			}
			renewed, err := db.RenewNativeGatewayGrant(t.Context(), req.GatewayID, grant.TunnelID)
			if err != nil || renewed.TunnelID != grant.TunnelID || renewed.Target != grant.Target {
				t.Fatal("valid renewal", err)
			}
			for _, sql := range []string{
				`UPDATE gateway_session_tickets SET target_host='10.31.0.111'`,
				`UPDATE gateway_session_tickets SET gateway_id='gw_othernode'`,
				`UPDATE gateway_session_tickets SET policy_revision=2`,
				`UPDATE gateway_session_tickets SET consumed_at=NULL,tunnel_id=NULL,lease_until=NULL`,
				`UPDATE desktop_connection_sessions SET native_session_digest=NULL`,
			} {
				_, err = db.pool.Exec(t.Context(), sql)
				var pg *pgconn.PgError
				if !errors.As(err, &pg) || pg.Code != "23514" {
					t.Fatalf("authority rewrite accepted: %s: %v", sql, err)
				}
			}
			for range 2 {
				if err = db.CloseNativeGatewayGrant(t.Context(), req.GatewayID, grant.TunnelID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = db.RenewNativeGatewayGrant(t.Context(), req.GatewayID, grant.TunnelID); !errors.Is(err, ErrNotFound) {
				t.Fatal("closed tunnel revived", err)
			}
			if _, err = db.pool.Exec(t.Context(), `UPDATE gateway_session_tickets SET closed_at=NULL`); err == nil {
				t.Fatal("closed tunnel history reset")
			}
			var events string
			if err = db.pool.QueryRow(t.Context(), `SELECT string_agg(event_type,',' ORDER BY id) FROM audit_events WHERE event_type LIKE 'gateway.%'`).Scan(&events); err != nil || events != "gateway.ticket_created,gateway.tunnel_authorized,gateway.tunnel_closed" {
				t.Fatal("audit not atomic/idempotent", events, err)
			}
		})
	}
}

func TestNativeGatewayOneTimeRedemptionAcrossStoreInstances(t *testing.T) {
	db, req := gatewayFixture(t, true)
	other, err := Open(t.Context(), db.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ticket, err := db.CreateNativeGatewayTicket(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 24)
	for i := range 24 {
		go func(i int) {
			<-start
			store := db
			if i%2 == 1 {
				store = other
			}
			_, err := store.RedeemNativeGatewayTicket(t.Context(), req.GatewayID, ticket.Token)
			results <- err
		}(i)
	}
	close(start)
	succeeded := 0
	for range 24 {
		err := <-results
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("one ticket produced %d successful transports", succeeded)
	}
	var consumed, audits int
	if err = db.pool.QueryRow(t.Context(), `SELECT count(*) FROM gateway_session_tickets WHERE consumed_at IS NOT NULL`).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if err = db.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE event_type='gateway.tunnel_authorized'`).Scan(&audits); err != nil || consumed != 1 || audits != 1 {
		t.Fatal("redemption was not atomic", consumed, audits, err)
	}
}

func TestNativeGatewayRechecksCurrentAuthority(t *testing.T) {
	for _, phase := range []string{"unspent", "active"} {
		for _, reason := range []string{"logout", "native_expired", "user_disabled", "desktop_disabled", "access_removed", "binding_disabled", "profile_disabled", "profile_platform", "policy_pending", "policy_reapplied", "guest_revocation", "native_pending", "connection_revoking"} {
			t.Run(phase+"/"+reason, func(t *testing.T) {
				db, req := gatewayFixture(t, true)
				ticket, err := db.CreateNativeGatewayTicket(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				var grant NativeGatewayGrant
				if phase == "active" {
					grant, err = db.RedeemNativeGatewayTicket(t.Context(), req.GatewayID, ticket.Token)
					if err != nil {
						t.Fatal(err)
					}
				}
				switch reason {
				case "logout":
					err = db.RevokeNativeSession(t.Context(), req.NativeDigest)
				case "native_expired":
					_, err = db.pool.Exec(t.Context(), `UPDATE native_sessions SET expires_at=statement_timestamp()-interval '1 second' WHERE token_digest=$1`, req.NativeDigest)
				case "user_disabled":
					_, err = db.pool.Exec(t.Context(), `UPDATE users SET disabled=true WHERE id='native-a'`)
				case "desktop_disabled":
					_, err = db.pool.Exec(t.Context(), `UPDATE managed_desktops SET enabled=false WHERE vmid=9001`)
				case "access_removed":
					_, err = db.DeleteDesktopAssignment(t.Context(), "user", "native-a", 9001)
				case "binding_disabled":
					_, err = db.pool.Exec(t.Context(), `UPDATE guest_identity_bindings SET state='disabled' WHERE desktop_vmid=9001 AND user_id='native-a'`)
				case "profile_disabled":
					_, err = db.pool.Exec(t.Context(), `UPDATE identity_profiles SET enabled=false WHERE id='managed-local-linux'`)
				case "profile_platform":
					_, err = db.pool.Exec(t.Context(), `UPDATE identity_profiles SET platform='windows' WHERE id='managed-local-linux'`)
				case "policy_pending", "policy_reapplied":
					var policy DesktopAccessPolicy
					policy, err = db.PutDesktopAccessPolicy(t.Context(), 9001, "standard", false, false, true, "native-a")
					if err == nil && reason == "policy_reapplied" {
						_, err = db.MarkDesktopAccessPolicyApplied(t.Context(), 9001, policy.DesiredRevision, "linux")
					}
				case "guest_revocation":
					_, err = db.pool.Exec(t.Context(), `INSERT INTO guest_identity_revocations(desktop_vmid,user_id,guest_username,reason) SELECT desktop_vmid,user_id,guest_username,'credential_revoked' FROM guest_identity_bindings WHERE desktop_vmid=9001`)
				case "native_pending":
					var a NativeGuestAccount
					a, err = db.NativeGuestAccount(t.Context(), 9001, "native-a")
					if err == nil {
						_, err = db.RetireNativeGuestCredential(t.Context(), a)
					}
				case "connection_revoking":
					_, err = db.BeginRevokeDesktopConnection(t.Context(), req.ConnectionID, "native-a")
				}
				if err != nil {
					t.Fatal(err)
				}
				if phase == "active" {
					_, err = db.RenewNativeGatewayGrant(t.Context(), req.GatewayID, grant.TunnelID)
				} else {
					_, err = db.RedeemNativeGatewayTicket(t.Context(), req.GatewayID, ticket.Token)
				}
				if !errors.Is(err, ErrNotFound) {
					t.Fatal("invalidated authority remained usable", err)
				}
				if _, err = db.CreateNativeGatewayTicket(t.Context(), req); !errors.Is(err, ErrNotFound) {
					t.Fatal("invalid authority minted another ticket", err)
				}
			})
		}
	}
}

func TestNativeGatewayExpiryAfterWaitingForAuthorizationLock(t *testing.T) {
	db, req := gatewayFixture(t, true)
	ticket, err := db.CreateNativeGatewayTicket(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := db.beginAccessChange(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = blocker.Exec(t.Context(), `UPDATE native_sessions SET expires_at=statement_timestamp()+interval '600 milliseconds' WHERE token_digest=$1`, req.NativeDigest); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := db.RedeemNativeGatewayTicket(t.Context(), req.GatewayID, ticket.Token)
		result <- err
	}()
	// Hold the actual shared authorization lock across credential expiry.
	time.Sleep(850 * time.Millisecond)
	if err = blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, ErrNotFound) {
		t.Fatal("transaction-start clock extended expired authorization", err)
	}
}

func TestNativeGatewayExpiredLeaseCannotResume(t *testing.T) {
	db, req := gatewayFixture(t, true)
	ticket, err := db.CreateNativeGatewayTicket(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.RedeemNativeGatewayTicket(t.Context(), req.GatewayID, ticket.Token)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(grant.ValidFor + 100*time.Millisecond)
	if _, err = db.RenewNativeGatewayGrant(t.Context(), req.GatewayID, grant.TunnelID); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired transport lease revived", err)
	}
}

func TestNativeGatewayRejectsUnknownConnectionOriginAndUnsafeTarget(t *testing.T) {
	db, a, _ := nativeAccountFixture(t, "linux")
	policy, err := db.EnsureDesktopAccessPolicy(t.Context(), a.DesktopVMID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkDesktopAccessPolicyApplied(t.Context(), a.DesktopVMID, policy.DesiredRevision, "linux"); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("valid device, unknown old connection origin"))
	if err := db.CreateNativeSession(t.Context(), digest[:], a.UserID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	conn, err := db.CreateDesktopConnectionSession(t.Context(), DesktopConnectionSession{ID: "conn_old_gateway", UserID: a.UserID, DesktopVMID: a.DesktopVMID, GuestUsername: a.GuestUsername, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	cert := sha256.Sum256([]byte("certificate"))
	req := NativeGatewayTicketRequest{ConnectionID: conn.ID, NativeDigest: digest[:], GatewayID: "gw_testprimary", Target: netip.MustParseAddr("10.31.0.110"), CertificateSHA256: cert[:], PolicyRevision: policy.DesiredRevision}
	if _, err = db.CreateNativeGatewayTicket(t.Context(), req); !errors.Is(err, ErrNotFound) {
		t.Fatal("adopted legacy session origin", err)
	}
	if _, err = db.pool.Exec(t.Context(), `UPDATE desktop_connection_sessions SET native_session_digest=$1 WHERE id=$2`, digest[:], conn.ID); err == nil {
		t.Fatal("unknown legacy origin was backfilled")
	}
}

func TestNativeGatewayRejectsUnsafeInputsForAuthorizedConnection(t *testing.T) {
	db, valid := gatewayFixture(t, true)
	for _, host := range []string{"127.0.0.1", "169.254.169.254", "8.8.8.8", "0.0.0.0", "::1", "::ffff:10.31.0.110"} {
		req := valid
		req.Target = netip.MustParseAddr(host)
		if _, err := db.CreateNativeGatewayTicket(t.Context(), req); !errors.Is(err, ErrNotFound) {
			t.Fatal("unsafe target accepted", host, err)
		}
	}
	for _, field := range []string{"policy", "certificate", "gateway", "origin"} {
		req := valid
		switch field {
		case "policy":
			req.PolicyRevision++
		case "certificate":
			req.CertificateSHA256 = nil
		case "gateway":
			req.GatewayID = "client-selected"
		case "origin":
			req.NativeDigest = nil
		}
		if _, err := db.CreateNativeGatewayTicket(t.Context(), req); !errors.Is(err, ErrNotFound) {
			t.Fatal("invalid ticket input accepted", field, err)
		}
	}
	if _, err := db.CreateNativeGatewayTicket(t.Context(), valid); err != nil {
		t.Fatal("negative checks were masked by unrelated fixture rejection", err)
	}
}

func TestNativeGatewayExpiredTicketCannotRedeemWithLiveAuthority(t *testing.T) {
	db, req := gatewayFixture(t, true)
	secret, err := auth.OpaqueToken(32)
	if err != nil {
		t.Fatal(err)
	}
	token := "gwt_" + secret
	// Insert a historically issued ticket; do not mutate immutable timestamps or
	// shorten Native authorization, which would mask a missing ticket TTL check.
	_, err = db.pool.Exec(t.Context(), `INSERT INTO gateway_session_tickets
 (token_digest,connection_id,gateway_id,target_host,target_port,certificate_sha256,policy_revision,created_at,expires_at,session_deadline)
 VALUES($1,$2,$3,$4,3389,$5,$6,statement_timestamp()-interval '31 seconds',statement_timestamp()-interval '1 second',statement_timestamp()+interval '1 hour')`,
		auth.TokenDigest(token), req.ConnectionID, req.GatewayID, req.Target.String(), req.CertificateSHA256, req.PolicyRevision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.RedeemNativeGatewayTicket(t.Context(), req.GatewayID, token); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired one-time ticket redeemed", err)
	}
	var count int
	if err = db.pool.QueryRow(t.Context(), `SELECT count(*) FROM (`+gatewayAccess+`) a WHERE a.id=$1`, req.ConnectionID).Scan(&count); err != nil || count != 1 {
		t.Fatal("ticket expiry failure was masked by invalid current authority", err)
	}
}

func TestNativeGatewayTokenParserRejectsNonCanonicalSecrets(t *testing.T) {
	secret, err := auth.OpaqueToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if digest, err := gatewayTokenDigest("gwt_" + secret); err != nil || len(digest) != 32 {
		t.Fatal(err)
	}
	for _, token := range []string{"", secret, "gwt_" + secret + "=", "gwt_" + strings.Repeat("a", 42), "gwt_" + strings.Repeat("!", 43), "GWT_" + secret, "gwt_" + strings.Repeat("A", 42) + "B"} {
		if _, err = gatewayTokenDigest(token); err == nil {
			t.Fatal("accepted invalid/noncanonical ticket")
		}
	}
}
