package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

type controlFixtureBackend struct {
	*testAuthority
	pings atomic.Int64
}

func (b *controlFixtureBackend) Ping(context.Context) error { b.pings.Add(1); return nil }

func realControlTLS(t *testing.T, backend ControlBackend) (*httptest.Server, *ControlClient, tlsFixtureCA, tls.Certificate) {
	t.Helper()
	ca := newTLSCA(t)
	clientCert := ca.issue(t, true, time.Now().Add(time.Hour))
	handler, err := NewControlHandler(backend, enrollment(t, clientCert))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS, err = ControlServerTLS(ca.issue(t, false, time.Now().Add(time.Hour)), ca.pool)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	client, err := NewControlClient(server.URL, "gw_testprimary", &tls.Config{Certificates: []tls.Certificate{clientCert}, RootCAs: ca.pool})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return server, client, ca, clientCert
}

func validTestTicket(t *testing.T) string {
	t.Helper()
	secret, err := auth.OpaqueToken(32)
	if err != nil {
		t.Fatal(err)
	}
	return "gwt_" + secret
}

func TestControlTLSActualCertificateAndBoundedProtocol(t *testing.T) {
	backend := &controlFixtureBackend{testAuthority: fixtureAuthority()}
	server, client, ca, clientCert := realControlTLS(t, backend)
	if err := client.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	grant, err := client.RedeemNativeGatewayTicket(t.Context(), "gw_testprimary", validTestTicket(t))
	if err != nil || grant.TunnelID != fixtureGrant().TunnelID || grant.PolicyRevision != 1 || grant.ValidFor != 600*time.Millisecond {
		t.Fatal("valid mTLS exchange failed", err)
	}
	if _, err = client.RenewNativeGatewayGrant(t.Context(), "gw_testprimary", grant.TunnelID); err != nil {
		t.Fatal(err)
	}
	if err = client.CloseNativeGatewayGrant(t.Context(), "gw_testprimary", grant.TunnelID); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"no_client_certificate", "unregistered_same_ca", "untrusted_client_ca", "wrong_eku"} {
		t.Run(kind, func(t *testing.T) {
			config := &tls.Config{RootCAs: ca.pool, MinVersion: tls.VersionTLS13}
			switch kind {
			case "unregistered_same_ca":
				config.Certificates = []tls.Certificate{ca.issue(t, true, time.Now().Add(time.Hour))}
			case "untrusted_client_ca":
				config.Certificates = []tls.Certificate{newTLSCA(t).issue(t, true, time.Now().Add(time.Hour))}
			case "wrong_eku":
				config.Certificates = []tls.Certificate{ca.issue(t, false, time.Now().Add(time.Hour))}
			}
			transport := &http.Transport{TLSClientConfig: config}
			defer transport.CloseIdleConnections()
			httpClient := &http.Client{Transport: transport, Timeout: time.Second}
			before := backend.pings.Load()
			res, err := httpClient.Get(server.URL + ControlReadyPath)
			if err == nil {
				defer res.Body.Close()
				if res.StatusCode != http.StatusForbidden {
					t.Fatal("unregistered certificate reached authority", res.StatusCode)
				}
			}
			if backend.pings.Load() != before {
				t.Fatal("unverified identity invoked backend")
			}
		})
	}
	ticket := validTestTicket(t)
	for _, body := range []string{`{}`, `{"ticket":"` + ticket + `","gateway_id":"gw_othernode"}`, `{"ticket":"` + ticket + `","ticket":"` + ticket + `"}`, `{"ticket":"` + ticket + `"} {}`, `{"ticket":null}`, `{"ticket":"` + strings.Repeat("x", 2000) + `"}`} {
		before := backend.issues.Load()
		res, err := client.client.Post(server.URL+ControlRedeemPath, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest || backend.issues.Load() != before || bytes.Contains(data, []byte(ticket)) {
			t.Fatal("malformed/ambiguous request consumed a ticket or reflected it")
		}
	}
	for _, endpoint := range []string{strings.Replace(server.URL, "https:", "http:", 1), server.URL + "?ticket=secret", server.URL + "/api", "https://user:secret@localhost"} {
		if _, err := NewControlClient(endpoint, "gw_testprimary", &tls.Config{RootCAs: ca.pool, Certificates: []tls.Certificate{clientCert}}); !errors.Is(err, ErrConfiguration) {
			t.Fatal("unsafe endpoint accepted")
		}
	}
}

func TestControlRejectsForwardedCertificateOnPlainHTTP(t *testing.T) {
	backend := &controlFixtureBackend{testAuthority: fixtureAuthority()}
	ca := newTLSCA(t)
	handler, err := NewControlHandler(backend, enrollment(t, ca.issue(t, true, time.Now().Add(time.Hour))))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, ControlReadyPath, nil)
	request.Header.Set("X-Gateway-ID", "gw_testprimary")
	request.Header.Set("X-Forwarded-Client-Cert", "trusted")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || backend.pings.Load() != 0 {
		t.Fatal("forwarded header replaced actual mTLS")
	}
}

func TestControlClientNeverFollowsRedirectOrTrustsUnsafeGrant(t *testing.T) {
	backend := &controlFixtureBackend{testAuthority: fixtureAuthority()}
	server, client, ca, cert := realControlTLS(t, backend)
	var destinationCalls atomic.Int64
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	// A dedicated second TLS server with the same test CA exercises the real
	// HTTP transport without replacing or mutating a running handler.
	for _, kind := range []string{"redirect", "oversized", "unknown_field", "public_target", "long_lease", "wrong_gateway", "bad_content_type"} {
		t.Run(kind, func(t *testing.T) {
			s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "redirect" {
					http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if kind == "oversized" {
					_, _ = w.Write([]byte(strings.Repeat("x", 4097)))
					return
				}
				grant := fixtureGrant()
				wire := map[string]any{"connection_id": grant.ConnectionID, "gateway_id": grant.GatewayID, "tunnel_id": grant.TunnelID,
					"target": grant.Target.String(), "certificate_sha256": strings.Repeat("ab", 32), "policy_revision": 1, "valid_for_ms": 5000}
				switch kind {
				case "unknown_field":
					wire["target_port"] = 22
				case "public_target":
					wire["target"] = "8.8.8.8"
				case "long_lease":
					wire["valid_for_ms"] = 5001
				case "wrong_gateway":
					wire["gateway_id"] = "gw_othernode"
				case "bad_content_type":
					w.Header().Set("Content-Type", "text/plain")
				}
				_ = json.NewEncoder(w).Encode(wire)
			}))
			s.TLS = server.TLS.Clone()
			s.StartTLS()
			defer s.Close()
			c, err := NewControlClient(s.URL, "gw_testprimary", &tls.Config{RootCAs: ca.pool, Certificates: []tls.Certificate{cert}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.RedeemNativeGatewayTicket(t.Context(), "gw_testprimary", validTestTicket(t)); !errors.Is(err, ErrAuthorization) {
				t.Fatal("unsafe control response accepted", err)
			}
		})
	}
	if destinationCalls.Load() != 0 || backend.issues.Load() != 0 {
		t.Fatal("redirect escaped authenticated control origin")
	}
	_ = client
}

func TestControlBackendErrorsAreNotReflected(t *testing.T) {
	backend := &controlFixtureBackend{testAuthority: fixtureAuthority()}
	backend.redeem = func(context.Context) (store.NativeGatewayGrant, error) {
		return store.NativeGatewayGrant{}, errors.New("private backend credential and query")
	}
	_, client, _, _ := realControlTLS(t, backend)
	_, err := client.RedeemNativeGatewayTicket(t.Context(), "gw_testprimary", validTestTicket(t))
	if !errors.Is(err, ErrAuthorization) || strings.Contains(err.Error(), "private") {
		t.Fatal("private backend error escaped")
	}
}

func TestControlExpiryRecheckedOnExistingTLSConnection(t *testing.T) {
	backend := &controlFixtureBackend{testAuthority: fixtureAuthority()}
	ca := newTLSCA(t)
	clientCert := ca.issue(t, true, time.Now().Add(2*time.Second))
	handler, err := NewControlHandler(backend, enrollment(t, clientCert))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS, err = ControlServerTLS(ca.issue(t, false, time.Now().Add(time.Hour)), ca.pool)
	if err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	defer server.Close()
	client, err := NewControlClient(server.URL, "gw_testprimary", &tls.Config{RootCAs: ca.pool, Certificates: []tls.Certificate{clientCert}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(clientCert.Leaf.NotAfter) + 20*time.Millisecond)
	if err = client.Ping(t.Context()); !errors.Is(err, ErrAuthorization) || backend.pings.Load() != 1 {
		t.Fatal("expired client identity survived on a keep-alive connection", err)
	}
}

func TestControlRejectsAmbiguousEnrollmentAndWireObjects(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	for _, data := range []string{
		`{"gw_testprimary":["` + pin + `"],"gw_testprimary":["` + strings.Repeat("bc", 32) + `"]}`,
		`{"gw_testprimary":["` + pin + `"],"gw_othernode":["` + pin + `"]}`,
		`{"gw_testprimary":["` + pin + `","` + pin + `"]}`,
		`{"bad_id":["` + pin + `"]}`, `{}`, `null`, `{"gw_testprimary":[]}`,
	} {
		if _, err := ParsePeerPins([]byte(data)); !errors.Is(err, ErrConfiguration) {
			t.Fatal("ambiguous Gateway enrollment accepted")
		}
	}
	for _, data := range []string{`{"target":"10.31.0.110","target":"127.0.0.1"}`, `{} {}`, `[]`} {
		if uniqueObject([]byte(data)) {
			t.Fatal("ambiguous control response accepted")
		}
	}
}
