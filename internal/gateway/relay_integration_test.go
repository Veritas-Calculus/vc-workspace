package gateway

import (
	"crypto/sha256"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
	"github.com/jackc/pgx/v5"
)

func postgresRelayFixture(t *testing.T) (*store.Store, string, [32]byte, store.NativeGatewayTicket) {
	t.Helper()
	dsn := testdb.URL(t)
	db, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateLocalUser(t.Context(), store.User{ID: "relay-test-user", Username: "relay-test-user", PasswordHash: "unused"}); err != nil {
		t.Fatal(err)
	}
	if err = db.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: 9001, Node: "fixture", OSFamily: "linux", DisplayName: "TCP lab"}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.PutDesktopAssignment(t.Context(), store.DesktopAssignment{SubjectType: "user", SubjectID: "relay-test-user", DesktopVMID: 9001}); err != nil {
		t.Fatal(err)
	}
	username := store.NativeGuestUsername("relay-test-user")
	if _, err = db.PutGuestIdentityBinding(t.Context(), store.GuestIdentityBinding{DesktopVMID: 9001, UserID: "relay-test-user", GuestUsername: username, ProfileID: "managed-local-linux", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	policy, err := db.EnsureDesktopAccessPolicy(t.Context(), 9001)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkDesktopAccessPolicyApplied(t.Context(), 9001, policy.DesiredRevision, "linux"); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("isolated relay Native session"))
	if err = db.CreateNativeSession(t.Context(), digest[:], "relay-test-user", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	bound, err := db.BindNativeGuestAccount(t.Context(), store.NativeGuestAccount{DesktopVMID: 9001, UserID: "relay-test-user", GuestUsername: username, OSFamily: "linux", GuestUID: 1001}, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	pending, err := db.BeginNativeGuestCredential(t.Context(), bound, "conn_relay_integration", time.Now().Add(time.Hour).Truncate(time.Second), digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CompleteNativeGuestConnection(t.Context(), pending); err != nil {
		t.Fatal(err)
	}
	grant := fixtureGrant()
	ticket, err := db.CreateNativeGatewayTicket(t.Context(), store.NativeGatewayTicketRequest{ConnectionID: pending.ConnectionID, NativeDigest: digest[:], GatewayID: grant.GatewayID,
		Target: grant.Target, CertificateSHA256: grant.CertificateSHA256, PolicyRevision: policy.DesiredRevision})
	if err != nil {
		t.Fatal(err)
	}
	return db, dsn, digest, ticket
}

func TestRelayPostgresAuthorityActuallyClosesTCP(t *testing.T) {
	for _, reason := range []string{"native_logout", "assignment_removed", "policy_updated", "database_unavailable"} {
		t.Run(reason, func(t *testing.T) {
			db, dsn, digest, ticket := postgresRelayFixture(t)
			var err error
			client, guest, done, _ := startRelay(t, fixtureRelay(t, db), ticket.Token)
			exchange(t, client, guest, "authorized data from real database")
			exchange(t, guest, client, "bidirectional data before revocation")
			started := time.Now()
			switch reason {
			case "native_logout":
				err = db.RevokeNativeSession(t.Context(), digest[:])
			case "assignment_removed":
				_, err = db.DeleteDesktopAssignment(t.Context(), "user", "relay-test-user", 9001)
			case "policy_updated":
				var changed store.DesktopAccessPolicy
				changed, err = db.PutDesktopAccessPolicy(t.Context(), 9001, "standard", false, false, true, "relay-test-user")
				if err == nil {
					_, err = db.MarkDesktopAccessPolicyApplied(t.Context(), 9001, changed.DesiredRevision, "linux")
				}
			case "database_unavailable":
				db.Close() // real authority pool unavailable, not a fake success response
			}
			if err != nil {
				t.Fatal(err)
			}
			// Observe socket closure against the actual 5 s lease, independently
			// of the later (up to 1 s) closure-audit RPC. Renewal normally rejects
			// at half-lease, but that is not a 3 s SLA under concurrent DB load.
			_ = client.SetReadDeadline(started.Add(store.GatewayAuthorizationWindow + 500*time.Millisecond))
			_, readErr := client.Read(make([]byte, 1))
			var network net.Error
			if readErr == nil || (errors.As(readErr, &network) && network.Timeout()) {
				t.Fatal("database invalidation left the socket open past its lease", readErr)
			}
			t.Logf("actual client socket closed after %s", time.Since(started).Round(time.Millisecond))
			err = awaitResult(t, done)
			if !errors.Is(err, ErrAuthorization) && !errors.Is(err, ErrLeaseExpired) && !errors.Is(err, ErrTransport) {
				t.Fatal("database invalidation did not stop the actual transport", err)
			}
			assertClosed(t, client)
			assertClosed(t, guest)
			// A separate DB connection independently verifies audit and closure;
			// closing the authority pool never shuts down the PostgreSQL server.
			check, err := pgx.Connect(t.Context(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer check.Close(t.Context())
			var closed bool
			var events int
			if err = check.QueryRow(t.Context(), `SELECT closed_at IS NOT NULL FROM gateway_session_tickets`).Scan(&closed); err != nil {
				t.Fatal(err)
			}
			if err = check.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE event_type LIKE 'gateway.%'`).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if reason == "database_unavailable" {
				if closed || events != 2 {
					t.Fatal("unavailable DB falsely acknowledged closure", closed, events)
				}
			} else if !closed || events != 3 {
				t.Fatal("closure not committed/audited exactly once", closed, events)
			}
		})
	}
}
