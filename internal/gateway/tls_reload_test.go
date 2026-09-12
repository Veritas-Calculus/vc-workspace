package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Model the mounted Kubernetes Secret layout, including replacement of the
// ..data symlink. Test-owned keys never leave t.TempDir or enter system trust.
func projectTLS(t *testing.T, root string, cert tls.Certificate, ca []byte, enrolled ...tls.Certificate) tlsFiles {
	t.Helper()
	dir, err := os.MkdirTemp(root, "generation-")
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	var chain []byte
	for _, der := range cert.Certificate {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	var pins []string
	for _, leaf := range enrolled {
		sum := sha256.Sum256(leaf.Certificate[0])
		pins = append(pins, hex.EncodeToString(sum[:]))
	}
	peers, err := json.Marshal(map[string][]string{"gw_testprimary": pins})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string][]byte{"tls.crt": chain, "tls.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), "ca.crt": ca, "peers.json": peers}
	defer clear(values["tls.key"])
	for name, data := range values {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			if err := os.Symlink(filepath.Join("..data", name), path); err != nil {
				t.Fatal(err)
			}
		}
	}
	staged := filepath.Join(root, "..data-next")
	if err := os.Symlink(filepath.Base(dir), staged); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	return tlsFiles{filepath.Join(root, "tls.crt"), filepath.Join(root, "tls.key"), filepath.Join(root, "ca.crt"), filepath.Join(root, "peers.json")}
}

func TestTLSReloadControlRotation(t *testing.T) {
	a, b := newTLSCA(t), newTLSCA(t)
	deadline := time.Now().Add(time.Hour)
	serverA, serverB := a.issue(t, false, deadline), b.issue(t, false, deadline)
	old, next, nextCA := a.issue(t, true, deadline), a.issue(t, true, deadline), b.issue(t, true, deadline)
	serverRoot, clientRoot := t.TempDir(), t.TempDir()
	sf := projectTLS(t, serverRoot, serverA, a.pem, old)
	cf := projectTLS(t, clientRoot, old, a.pem)
	backend := &controlFixtureBackend{testAuthority: fixtureAuthority()}
	handler, err := NewControlHandler(backend, enrollment(t, old))
	if err != nil {
		t.Fatal(err)
	}
	handler.(*controlHandler).identity = sf.identity
	server := httptest.NewUnstartedServer(handler)
	server.TLS, err = sf.serverConfig()
	if err != nil {
		t.Fatal(err)
	}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	newClient := func(cert tls.Certificate, roots *x509.CertPool) *ControlClient {
		c, err := NewControlClient(server.URL, "gw_testprimary", &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: roots})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Close)
		return c
	}
	current := newClient(old, a.pool)
	if err := current.reloadFrom(cf); err != nil {
		t.Fatal(err)
	}
	// Keep one real WSS -> loopback TCP stream alive throughout both rotations.
	// The control backend is a model; no real database, ticket or OS is involved.
	relay := fixtureRelay(t, current)
	upstream, guest := tcpPair(t)
	relay.dial = func(context.Context, string, string) (net.Conn, error) { return upstream, nil }
	publicRoot := t.TempDir()
	publicFiles := projectTLS(t, publicRoot, serverA, nil)
	publicFiles.ca = ""
	public := httptest.NewUnstartedServer(nil)
	publicTransport, err := NewTransport(t.Context(), relay, public.Listener.Addr().String(), current.Ping)
	if err != nil {
		t.Fatal(err)
	}
	public.Config.Handler = publicTransport
	public.TLS, err = publicFiles.serverConfig()
	if err != nil {
		t.Fatal(err)
	}
	public.StartTLS()
	t.Cleanup(public.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := publicTransport.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	publicHTTPTransport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: a.pool}}
	t.Cleanup(publicHTTPTransport.CloseIdleConnections)
	publicClient := &http.Client{Transport: publicHTTPTransport, Timeout: 2 * time.Second}
	ws := dialWebSocket(t, public, publicClient)
	stream := authorizeWebSocket(t, ws, validTestTicket(t))
	exchange(t, stream, guest, "before identity rotation")
	oldClient, nextClient := newClient(old, a.pool), newClient(next, a.pool)
	ping := func(c *ControlClient, want bool, reuse bool) {
		t.Helper()
		reused := false
		ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }})
		before := backend.pings.Load()
		err := c.Ping(ctx)
		if (err == nil) != want || (!want && backend.pings.Load() != before) || (reuse && !reused) {
			t.Fatalf("ping: success=%v wanted=%v reused=%v wanted-reuse=%v backend-delta=%d", err == nil, want, reused, reuse, backend.pings.Load()-before)
		}
	}
	ping(current, true, false)
	ping(current, true, true)
	ping(oldClient, true, false)
	ping(nextClient, false, false) // same CA alone is insufficient
	projectTLS(t, serverRoot, serverA, a.pem, old, next)
	ping(nextClient, true, false)
	ping(oldClient, true, true)
	projectTLS(t, clientRoot, next, a.pem)
	ping(current, true, false)
	projectTLS(t, serverRoot, serverA, a.pem, next)
	ping(oldClient, false, true) // revoked pin on the exact pre-existing TLS socket
	ping(current, true, true)

	// Prepare both trust roots and pins before switching either identity.
	overlap := append(append([]byte{}, a.pem...), b.pem...)
	projectTLS(t, serverRoot, serverA, overlap, next, nextCA)
	projectTLS(t, clientRoot, nextCA, overlap)
	ping(current, true, false)
	projectTLS(t, serverRoot, serverB, overlap, next, nextCA)
	pool, err := strictCAPool(overlap)
	if err != nil {
		t.Fatal(err)
	}
	fresh := newClient(nextCA, pool)
	var observed [32]byte
	ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{TLSHandshakeDone: func(s tls.ConnectionState, err error) {
		if err == nil {
			observed = sha256.Sum256(s.PeerCertificates[0].Raw)
		}
	}})
	if err := fresh.Ping(ctx); err != nil || observed != sha256.Sum256(serverB.Certificate[0]) {
		t.Fatal("listener did not serve the new CA leaf without restart", err)
	}
	ping(nextClient, true, true)
	projectTLS(t, serverRoot, serverB, b.pem, next, nextCA)
	ping(nextClient, false, true) // pin is still enrolled; withdrawn CA must reject
	projectTLS(t, clientRoot, nextCA, b.pem)
	ping(current, true, false)
	projectTLS(t, serverRoot, serverB, b.pem, nextCA)
	ping(current, true, true)
	publicNext := a.issue(t, false, deadline)
	projectTLS(t, publicRoot, publicNext, nil)
	publicHTTPTransport.CloseIdleConnections()
	response, err := publicClient.Get(public.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || sha256.Sum256(response.TLS.PeerCertificates[0].Raw) != sha256.Sum256(publicNext.Certificate[0]) {
		t.Fatal("public listener did not serve its renewed leaf")
	}
	// Outlive the 600 ms model grant after rotation so open TCP alone cannot
	// masquerade as continued authorization with the new client certificate.
	previousRenewals := backend.renews.Load()
	until := time.Now().Add(2 * time.Second)
	for backend.renews.Load() < previousRenewals+3 && time.Now().Before(until) {
		time.Sleep(10 * time.Millisecond)
	}
	if backend.renews.Load() < previousRenewals+3 {
		t.Fatal("rotation stopped live tunnel authorization")
	}
	exchange(t, guest, stream, "same WSS after CA and leaf rotation")
	if backend.issues.Load() != 1 {
		t.Fatal("rotation redeemed the ticket again")
	}
	ctxClose, stopClose := context.WithTimeout(t.Context(), time.Second)
	if err := publicTransport.Close(ctxClose); err != nil {
		t.Fatal(err)
	}
	stopClose()
	assertClosed(t, guest)
	if backend.closes.Load() != 1 {
		t.Fatal("rotated tunnel was not acknowledged closed")
	}

	// Never fall back to previously good pins, trust roots or a key pair.
	for _, target := range []string{sf.peers, sf.ca, sf.key, cf.ca, cf.key} {
		if err := os.WriteFile(target, []byte("truncated private deployment configuration"), 0o600); err != nil {
			t.Fatal(err)
		}
		ping(current, false, false)
		projectTLS(t, serverRoot, serverB, b.pem, nextCA)
		projectTLS(t, clientRoot, nextCA, b.pem)
		ping(current, true, false)
	}
	projectTLS(t, clientRoot, b.issue(t, true, time.Now().Add(-time.Second)), b.pem)
	ping(current, false, false)
	projectTLS(t, clientRoot, nextCA, b.pem)
	ping(current, true, false)
	current.Close()
	ping(current, false, false)
}

func TestTLSReloadMalformedCAAndLeaf(t *testing.T) {
	ca := newTLSCA(t)
	leaf := ca.issue(t, true, time.Now().Add(time.Hour))
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Certificate[0]})
	for name, data := range map[string][]byte{
		"empty": nil, "leaf": leafPEM,
		"trailing":      append(append([]byte{}, ca.pem...), []byte("unexpected data")...),
		"truncated":     append(append([]byte{}, ca.pem...), []byte("-----BEGIN CERTIFICATE-----\nAA")...),
		"skipped block": append([]byte("-----BEGIN CERTIFICATE-----\ninvalid\n"), ca.pem...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := strictCAPool(data); err == nil {
				t.Fatal("malformed CA bundle accepted")
			}
		})
	}
	for _, client := range []bool{true, false} {
		wrongUsage := ca.issue(t, !client, time.Now().Add(time.Hour))
		f := projectTLS(t, t.TempDir(), wrongUsage, ca.pem)
		if _, _, err := f.load(client); err == nil {
			t.Fatal("wrong EKU accepted")
		}
		f = projectTLS(t, t.TempDir(), ca.issue(t, client, time.Now().Add(-time.Second)), ca.pem)
		if _, _, err := f.load(client); err == nil {
			t.Fatal("expired leaf accepted")
		}
	}
	copy := *leaf.Leaf
	copy.NotBefore = time.Now().Add(time.Hour)
	if validLeaf(&copy, x509.ExtKeyUsageClientAuth) {
		t.Fatal("future leaf accepted")
	}
}

func TestTLSReloadConcurrentRequests(t *testing.T) {
	backend := &controlFixtureBackend{testAuthority: fixtureAuthority()}
	server, _, ca, cert := realControlTLS(t, backend)
	f := projectTLS(t, t.TempDir(), cert, ca.pem)
	client, err := NewControlClient(server.URL, "gw_testprimary", &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: ca.pool})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	if err := client.reloadFrom(f); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for range 8 {
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				err := client.Ping(ctx)
				cancel()
				if err != nil {
					t.Error("concurrent reloaded control request", err)
					return
				}
			}
		})
	}
	workers.Wait()
	if backend.pings.Load() != 128 {
		t.Fatal("missing or replayed request", backend.pings.Load())
	}
}

func TestTLSReloadRejectsExpiredServerOnKeepAlive(t *testing.T) {
	ca := newTLSCA(t)
	clientCert := ca.issue(t, true, time.Now().Add(time.Hour))
	expires := time.Now().Truncate(time.Second).Add(2 * time.Second)
	serverCert := ca.issue(t, false, expires)
	root := t.TempDir()
	sf := projectTLS(t, root, serverCert, ca.pem, clientCert)
	backend := &controlFixtureBackend{testAuthority: fixtureAuthority()}
	handler, err := NewControlHandler(backend, enrollment(t, clientCert))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS, err = sf.serverConfig()
	if err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	cf := projectTLS(t, t.TempDir(), clientCert, ca.pem)
	client, err := NewControlClient(server.URL, "gw_testprimary", &tls.Config{Certificates: []tls.Certificate{clientCert}, RootCAs: ca.pool})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	if err := client.reloadFrom(cf); err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	projectTLS(t, root, ca.issue(t, false, time.Now().Add(time.Hour)), ca.pem, clientCert)
	time.Sleep(time.Until(expires.Add(20 * time.Millisecond)))
	reused := false
	ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }})
	if err := client.Ping(ctx); err == nil || !reused {
		t.Fatal("trusted authorization over an expired keep-alive server identity", err, reused)
	}
	// Rejection closes the stale connection; a new verified handshake recovers.
	if err := client.Ping(t.Context()); err != nil {
		t.Fatal("fresh renewed server identity failed", err)
	}
}
