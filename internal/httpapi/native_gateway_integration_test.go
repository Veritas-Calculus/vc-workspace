package httpapi

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/gateway"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/jackc/pgx/v5"
)

func nativeGatewayCertificate(t *testing.T) (pve.GuestExecResult, [32]byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, c, c, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pve.GuestExecResult{Stdout: base64.StdEncoding.EncodeToString(der) + "\n"}, sha256.Sum256(der)
}

func nativeGatewayRoute(t *testing.T, cidr string) *gateway.BrokerRoute {
	t.Helper()
	route, err := gateway.NewBrokerRoute("gw_testprimary", "https://gateway.example:8443", []netip.Prefix{netip.MustParsePrefix(cidr)}, gateway.PeerPins{[32]byte{1}: "gw_testprimary"})
	if err != nil {
		t.Fatal(err)
	}
	return route
}

func nativeGatewayRequest(s *Server, token, capability string) *httptest.ResponseRecorder {
	// Client attempts to supply an endpoint/certificate in the body and URL
	// have no authority. Only the trusted QGA observation/config may be signed.
	r := httptest.NewRequest("POST", "/api/v1/native/desktops/9001/connections?host=203.0.113.9", strings.NewReader(`{"gateway_id":"gw_attacker","host":"203.0.113.9","certificate_sha256":"untrusted"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	if capability != "" {
		r.Header.Set("X-VC-Workspace-Transport", capability)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestNativeGatewayBrokerDescriptorAndOneTimeAuthority(t *testing.T) {
	certificate, pin := nativeGatewayCertificate(t)
	s, _, _, token := nativeOrderCertificateFixture(t, &certificate)
	s.nativeGateway = nativeGatewayRoute(t, "10.0.0.0/24")
	w := nativeGatewayRequest(s, token, gateway.TunnelSubprotocol)
	if w.Code != 201 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("gateway descriptor rejected", w.Code, w.Body.String())
	}
	var descriptor struct {
		ID, Protocol, Host, Password string
		Gateway                      nativeGatewayConnection
		Policy                       nativeSessionPolicy `json:"session_policy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &descriptor); err != nil {
		t.Fatal(err)
	}
	if descriptor.Protocol != "rdp-gateway" || descriptor.Host != "10.0.0.42" || len(descriptor.Password) < 24 ||
		descriptor.Gateway.URL != "wss://gateway.example:8443/gateway/v1/rdp" || descriptor.Gateway.Subprotocol != gateway.TunnelSubprotocol || descriptor.Gateway.CertificateSHA256 != hex.EncodeToString(pin[:]) || len(descriptor.Gateway.Ticket) != 47 {
		t.Fatal("descriptor lost server-owned route/identity or protocol guard")
	}
	grant, err := s.store.RedeemNativeGatewayTicket(t.Context(), "gw_testprimary", descriptor.Gateway.Ticket)
	if err != nil || grant.ConnectionID != descriptor.ID || grant.Target.String() != "10.0.0.42" || !bytes.Equal(grant.CertificateSHA256, pin[:]) || grant.PolicyRevision != descriptor.Policy.Revision {
		t.Fatal("broker ticket did not bind actual descriptor", err)
	}
	if _, err := s.store.RedeemNativeGatewayTicket(t.Context(), "gw_testprimary", descriptor.Gateway.Ticket); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("ticket replay accepted", err)
	}
	if w := nativeOrderRequest(s, token, "POST", "auth/native/logout"); w.Code != 204 {
		t.Fatal("logout failed", w.Code)
	}
	if _, err := s.store.RenewNativeGatewayGrant(t.Context(), "gw_testprimary", grant.TunnelID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("logout did not revoke gateway authority", err)
	}
}

func TestNativeGatewayFailsBeforeCredentialOrTicket(t *testing.T) {
	for _, mode := range []string{"old_client", "wrong_protocol", "target_scope", "certificate_empty", "certificate_stderr", "certificate_exit", "windows", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			certificate, _ := nativeGatewayCertificate(t)
			s, e, user, token := nativeOrderCertificateFixture(t, &certificate)
			s.nativeGateway = nativeGatewayRoute(t, "10.0.0.0/24")
			capability, want := gateway.TunnelSubprotocol, http.StatusConflict
			switch mode {
			case "old_client":
				capability, want = "", 426
			case "wrong_protocol":
				capability, want = "rdp", 426
			case "target_scope":
				s.nativeGateway = nativeGatewayRoute(t, "10.31.0.0/24")
			case "certificate_empty":
				certificate.Stdout = ""
			case "certificate_stderr":
				certificate.Stderr = "private certificate diagnostic"
			case "certificate_exit":
				certificate.ExitCode = 1
			case "windows":
				if err := s.store.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: 9001, Node: "test", OSFamily: "windows", DisplayName: "Windows denied"}); err != nil {
					t.Fatal(err)
				}
			case "unauthorized":
				want = 404
				if _, err := s.store.DeleteDesktopAssignment(t.Context(), "user", user.ID, 9001); err != nil {
					t.Fatal(err)
				}
			}
			w := nativeGatewayRequest(s, token, capability)
			if w.Code != want || strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "gwt_") || strings.Contains(w.Body.String(), "private certificate") {
				t.Fatal("failed path exposed credential or wrong status", w.Code)
			}
			if len(e.operations) != 0 {
				t.Fatal("preflight failure mutated credentials", e.operations)
			}
			independent, err := pgx.Connect(t.Context(), e.databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer independent.Close(t.Context())
			var sessions, tickets int
			if err := independent.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM desktop_connection_sessions),(SELECT count(*) FROM gateway_session_tickets)`).Scan(&sessions, &tickets); err != nil || sessions != 0 || tickets != 0 {
				t.Fatal("failure persisted a connection/ticket", err)
			}
		})
	}
}

func TestNativeGatewayTicketFailureRollsBackGuestAcknowledgement(t *testing.T) {
	for _, mode := range []string{"ticket_insert", "ticket_audit", "policy_race", "native_logout"} {
		t.Run(mode, func(t *testing.T) {
			certificate, _ := nativeGatewayCertificate(t)
			s, e, user, token := nativeOrderCertificateFixture(t, &certificate)
			s.nativeGateway = nativeGatewayRoute(t, "10.0.0.0/24")
			independent, err := pgx.Connect(t.Context(), e.databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer independent.Close(t.Context())
			if mode == "ticket_insert" || mode == "ticket_audit" {
				table, condition := "gateway_session_tickets", "true"
				if mode == "ticket_audit" {
					table, condition = "audit_events", "NEW.event_type='gateway.ticket_created'"
				}
				_, err = independent.Exec(t.Context(), `CREATE FUNCTION reject_gateway_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RAISE EXCEPTION 'private injected failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_gateway_test BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_gateway_test()`)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				e.onIssue = func() {
					if mode == "native_logout" {
						err = s.store.DeleteNativeSession(t.Context(), auth.TokenDigest(token))
					} else {
						_, err = s.store.PutDesktopAccessPolicy(t.Context(), 9001, "standard", false, false, false, "")
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			w := nativeGatewayRequest(s, token, gateway.TunnelSubprotocol)
			if w.Code != 409 || strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "gwt_") || strings.Contains(w.Body.String(), "private injected") {
				t.Fatal("failed mint leaked a descriptor", w.Code)
			}
			var sessions, tickets, audits int
			if err := independent.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM desktop_connection_sessions),(SELECT count(*) FROM gateway_session_tickets),(SELECT count(*) FROM audit_events WHERE event_type='gateway.ticket_created')`).Scan(&sessions, &tickets, &audits); err != nil || sessions != 0 || tickets != 0 || audits != 0 {
				t.Fatal("partial connection/ticket/audit commit survived", sessions, tickets, audits, err)
			}
			a, err := s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
			if err != nil || a.State != "pending" || a.Operation != "revoke" || a.Revision != 2 {
				t.Fatal("failed acknowledgement lost higher durable recovery intent", err)
			}
			s.recoverDueNativeAccounts(t.Context())
			a, err = s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
			if err != nil || a.State != "applied" || a.Operation != "revoke" || strings.Join(e.operations, ",") != "issue,revoke" {
				t.Fatal("background recovery did not revoke without password replay", err, e.operations)
			}
		})
	}
}

func TestNativeGatewayTransportClosureRetiresCredentialAndPreservesDesktop(t *testing.T) {
	certificate, _ := nativeGatewayCertificate(t)
	s, e, user, token := nativeOrderCertificateFixture(t, &certificate)
	s.nativeGateway = nativeGatewayRoute(t, "10.0.0.0/24")
	w := nativeGatewayRequest(s, token, gateway.TunnelSubprotocol)
	if w.Code != 201 {
		t.Fatal("gateway connection failed", w.Code)
	}
	var descriptor struct {
		ID      string
		Gateway nativeGatewayConnection
	}
	if err := json.Unmarshal(w.Body.Bytes(), &descriptor); err != nil {
		t.Fatal(err)
	}
	grant, err := s.store.RedeemNativeGatewayTicket(t.Context(), "gw_testprimary", descriptor.Gateway.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	if due, err := s.store.DesktopConnectionsDueForRevocation(t.Context(), 10); err != nil || len(due) != 0 {
		t.Fatal("live transport retired", err)
	}
	if err := s.store.CloseNativeGatewayGrant(t.Context(), "gw_testprimary", grant.TunnelID); err != nil {
		t.Fatal(err)
	}
	// No DELETE from the native client: a crash must still retire credentials.
	s.revokeDueDesktopConnections(t.Context())
	a, err := s.store.NativeGuestAccount(t.Context(), 9001, user.ID)
	if err != nil || a.Operation != "retire" || a.State != "applied" || strings.Join(e.operations, ",") != "issue,retire" {
		t.Fatal("transport closure lost credential retirement or terminated desktop", err, e.operations)
	}
	if _, err := s.store.RenewNativeGatewayGrant(t.Context(), "gw_testprimary", grant.TunnelID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("closed transport resurrected", err)
	}
	if w := nativeGatewayRequest(s, token, gateway.TunnelSubprotocol); w.Code != 201 {
		t.Fatal("retained desktop could not receive fresh connection", w.Code)
	}
}
