package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/coder/websocket"
)

func writeTLSFixture(t *testing.T, cert tls.Certificate) (string, string) {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "certificate.pem"), filepath.Join(dir, "private.pem")
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func availableAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func TestGatewayRuntimeTLSListenersAndShutdown(t *testing.T) {
	ca := newTLSCA(t)
	serverCert := ca.issue(t, false, time.Now().Add(time.Hour))
	clientCert := ca.issue(t, true, time.Now().Add(time.Hour))
	certFile, keyFile := writeTLSFixture(t, serverCert)
	clientCertFile, clientKeyFile := writeTLSFixture(t, clientCert)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, ca.pem, 0o600); err != nil {
		t.Fatal(err)
	}
	peersFile := filepath.Join(t.TempDir(), "peers.json")
	pin := sha256.Sum256(clientCert.Certificate[0])
	if err := os.WriteFile(peersFile, []byte(`{"gw_testprimary":["`+hex.EncodeToString(pin[:])+`"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	controlAddress, publicAddress := availableAddress(t), availableAddress(t)
	for key, value := range map[string]string{
		"VC_WORKSPACE_GATEWAY_CONTROL_ADDR": controlAddress, "VC_WORKSPACE_GATEWAY_CONTROL_TLS_CERT_FILE": certFile,
		"VC_WORKSPACE_GATEWAY_CONTROL_TLS_KEY_FILE": keyFile, "VC_WORKSPACE_GATEWAY_CONTROL_CLIENT_CA_FILE": caFile,
		"VC_WORKSPACE_GATEWAY_CONTROL_PEERS_FILE": peersFile,
		"VC_WORKSPACE_GATEWAY_ADDR":               publicAddress, "VC_WORKSPACE_GATEWAY_PUBLIC_URL": "https://" + publicAddress,
		"VC_WORKSPACE_GATEWAY_ID": "gw_testprimary", "VC_WORKSPACE_GATEWAY_ALLOWED_TARGETS": "10.31.0.0/24", "VC_WORKSPACE_GATEWAY_MAX_CONNECTIONS": "2",
		"VC_WORKSPACE_GATEWAY_TLS_CERT_FILE": certFile, "VC_WORKSPACE_GATEWAY_TLS_KEY_FILE": keyFile,
		"VC_WORKSPACE_GATEWAY_CLIENT_CERT_FILE": clientCertFile, "VC_WORKSPACE_GATEWAY_CLIENT_KEY_FILE": clientKeyFile,
		"VC_WORKSPACE_GATEWAY_CONTROL_CA_FILE": caFile, "VC_WORKSPACE_GATEWAY_CONTROL_URL": "https://" + controlAddress,
	} {
		t.Setenv(key, value)
	}
	control, err := LoadControlListenerConfig()
	if err != nil || control == nil {
		t.Fatal("load control listener", err)
	}
	cfg, err := LoadRuntimeConfig()
	if err != nil {
		t.Fatal("load Gateway", err)
	}
	backend := &controlFixtureBackend{testAuthority: fixtureAuthority()}
	// This runtime test does not dial real Guest addresses. Its authority rejects
	// ticket use, while the separate DB/WSS test exercises the actual data path.
	backend.redeem = func(context.Context) (store.NativeGatewayGrant, error) {
		return store.NativeGatewayGrant{}, store.ErrNotFound
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	controlDone, gatewayDone := make(chan error, 1), make(chan error, 1)
	go func() { controlDone <- RunControlService(ctx, *control, backend, logger) }()
	go func() { gatewayDone <- RunService(ctx, cfg, logger) }()
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: ca.pool, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	deadline := time.Now().Add(4 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		res, err := client.Get("https://" + publicAddress + "/ready")
		if err == nil {
			res.Body.Close()
			ready = res.StatusCode == http.StatusNoContent
		}
		if ready {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready || backend.pings.Load() == 0 {
		t.Fatal("real TLS services did not become ready")
	}
	ws, _, err := websocket.Dial(t.Context(), "wss://"+publicAddress+TunnelPath,
		&websocket.DialOptions{HTTPClient: client, Subprotocols: []string{TunnelSubprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	request, stopRequest := context.WithTimeout(t.Context(), 3*time.Second)
	defer stopRequest()
	if err = ws.Write(request, websocket.MessageText, []byte(validTestTicket(t))); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ws.Read(request); err == nil || request.Err() != nil || backend.issues.Load() != 1 {
		t.Fatal("runtime service did not use its enrolled mTLS authority", err)
	}
	// Exercise the environment loaders and RunService wiring, not only the
	// standalone reload callbacks. Both services stay alive while files change.
	nextClient := ca.issue(t, true, time.Now().Add(time.Hour))
	nextServer := ca.issue(t, false, time.Now().Add(time.Hour))
	nextCertFile, nextKeyFile := writeTLSFixture(t, nextClient)
	nextServerFile, nextServerKey := writeTLSFixture(t, nextServer)
	nextPin := sha256.Sum256(nextClient.Certificate[0])
	if err := os.WriteFile(peersFile, []byte(`{"gw_testprimary":["`+hex.EncodeToString(pin[:])+`","`+hex.EncodeToString(nextPin[:])+`"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for destination, source := range map[string]string{clientCertFile: nextCertFile, clientKeyFile: nextKeyFile, certFile: nextServerFile, keyFile: nextServerKey} {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		err = os.WriteFile(destination, data, 0o600)
		clear(data)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(peersFile, []byte(`{"gw_testprimary":["`+hex.EncodeToString(nextPin[:])+`"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	transport.CloseIdleConnections()
	res, err := client.Get("https://" + publicAddress + "/ready")
	if err != nil {
		t.Fatal("runtime did not reload TLS configuration", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent || sha256.Sum256(res.TLS.PeerCertificates[0].Raw) != sha256.Sum256(nextServer.Certificate[0]) {
		t.Fatal("runtime retained withdrawn client/server credentials")
	}
	cancel()
	for _, done := range []<-chan error{controlDone, gatewayDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal("runtime shutdown", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("runtime TLS listener did not stop")
		}
	}
	if c, err := net.DialTimeout("tcp", publicAddress, 100*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("runtime kept listening after shutdown")
	}
}

func TestGatewayControlListenerDisabledUnlessCompletelyConfigured(t *testing.T) {
	for _, key := range []string{"VC_WORKSPACE_GATEWAY_CONTROL_ADDR", "VC_WORKSPACE_GATEWAY_CONTROL_TLS_CERT_FILE", "VC_WORKSPACE_GATEWAY_CONTROL_TLS_KEY_FILE", "VC_WORKSPACE_GATEWAY_CONTROL_CLIENT_CA_FILE", "VC_WORKSPACE_GATEWAY_CONTROL_PEERS_FILE"} {
		t.Setenv(key, "")
	}
	if cfg, err := LoadControlListenerConfig(); err != nil || cfg != nil {
		t.Fatal("Gateway control listener unexpectedly enabled", err)
	}
	t.Setenv("VC_WORKSPACE_GATEWAY_CONTROL_ADDR", "127.0.0.1:8444")
	if _, err := LoadControlListenerConfig(); !errors.Is(err, ErrConfiguration) {
		t.Fatal("partial control configuration accepted")
	}
	for _, address := range []string{":8443", "localhost:8443", "0.0.0.0:0", "0.0.0.0:-1", "0.0.0.0:65536", "0.0.0.0:https"} {
		if listenAddress(address) {
			t.Fatal("ambiguous listener accepted", address)
		}
	}
}

func TestGatewayLimitedListenerUnblocksShutdownAtCapacity(t *testing.T) {
	raw, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := &limitedListener{Listener: raw, slots: make(chan struct{}, 1), done: make(chan struct{})}
	defer listener.Close()
	peer, err := net.DialTimeout("tcp4", raw.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer accepted.Close()
	result := make(chan error, 1)
	go func() { _, err := listener.Accept(); result <- err }()
	listener.Close()
	select {
	case err := <-result:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener blocked on a full admission limit during shutdown")
	}
	accepted.Close()
	accepted.Close()
	if len(listener.slots) != 0 {
		t.Fatal("accepted socket leaked/double-released admission")
	}
}

func TestGatewayRejectsInsecureControlTLS(t *testing.T) {
	ca := newTLSCA(t)
	cert := ca.issue(t, true, time.Now().Add(time.Hour))
	for _, kind := range []string{"skip_verify", "missing_ca", "missing_certificate", "hostname_override", "key_log"} {
		config := &tls.Config{RootCAs: ca.pool, Certificates: []tls.Certificate{cert}}
		switch kind {
		case "skip_verify":
			config.InsecureSkipVerify = true
		case "missing_ca":
			config.RootCAs = nil
		case "missing_certificate":
			config.Certificates = nil
		case "hostname_override":
			config.ServerName = "other.invalid"
		case "key_log":
			config.KeyLogWriter = &strings.Builder{}
		}
		if _, err := NewControlClient("https://localhost:8444", "gw_testprimary", config); !errors.Is(err, ErrConfiguration) {
			t.Fatal("unsafe TLS configuration accepted", kind)
		}
	}
}
