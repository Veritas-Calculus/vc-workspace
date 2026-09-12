package gateway

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"sync"
	"time"
)

// File paths are fixed at startup. Mounted Secret updates may change their
// contents, but cannot change the endpoint, Gateway ID or target allowlist.
// Each load builds a new immutable configuration; malformed/partial updates
// fail closed, rather than silently retaining withdrawn trust material.
type tlsFiles struct {
	cert, key, ca, peers string
}

func (f tlsFiles) load(client bool) (*tls.Config, [32]byte, error) {
	var generation [32]byte
	pair, err := LoadCertificate(f.cert, f.key)
	if err != nil {
		return nil, generation, err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	usage := x509.ExtKeyUsageServerAuth
	if client {
		usage = x509.ExtKeyUsageClientAuth
	}
	if err != nil || !validLeaf(leaf, usage) {
		return nil, generation, ErrConfiguration
	}
	pair.Leaf = leaf
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair},
		SessionTicketsDisabled: true, NextProtos: []string{"http/1.1"}}
	hash := sha256.New()
	for _, der := range pair.Certificate {
		hash.Write(der)
	}
	if f.ca != "" {
		data, err := ReadConfigFile(f.ca)
		if err != nil {
			return nil, generation, err
		}
		pool, err := strictCAPool(data)
		if err != nil {
			return nil, generation, err
		}
		hash.Write(data)
		if client {
			cfg.RootCAs = pool
		} else {
			cfg.ClientCAs, cfg.ClientAuth = pool, tls.RequireAndVerifyClientCert
		}
	} else if client {
		return nil, generation, ErrConfiguration
	}
	copy(generation[:], hash.Sum(nil))
	return cfg, generation, nil
}

func validLeaf(leaf *x509.Certificate, usage x509.ExtKeyUsage) bool {
	if leaf == nil || leaf.IsCA || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return false
	}
	if len(leaf.ExtKeyUsage) == 0 {
		return true
	}
	for _, allowed := range leaf.ExtKeyUsage {
		if allowed == usage || allowed == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

func strictCAPool(data []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	for len(data) != 0 {
		// Only whitespace may surround PEM blocks; do not accept a valid prefix
		// of a truncated bundle or treat an unrelated leaf as a trust anchor.
		data = bytes.TrimSpace(data)
		if len(data) == 0 {
			break
		}
		if !bytes.HasPrefix(data, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, ErrConfiguration
		}
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 ||
			bytes.Count(data[:len(data)-len(rest)], []byte("-----BEGIN ")) != 1 {
			return nil, ErrConfiguration
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, ErrConfiguration
		}
		pool.AddCert(cert)
		data = rest
	}
	if len(pool.Subjects()) == 0 {
		return nil, ErrConfiguration
	}
	return pool, nil
}

func (f tlsFiles) serverConfig() (*tls.Config, error) {
	cfg, _, err := f.load(false)
	if err != nil {
		return nil, err
	}
	cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		next, _, err := f.load(false)
		return next, err
	}
	return cfg, nil
}

func (f tlsFiles) identity(state *tls.ConnectionState) string {
	cfg, _, err := f.load(false)
	if err != nil || cfg.ClientCAs == nil {
		return ""
	}
	data, err := ReadConfigFile(f.peers)
	if err != nil {
		return ""
	}
	pins, err := ParsePeerPins(data)
	if err != nil || verifyCurrentPeer(state, cfg.ClientCAs, "", x509.ExtKeyUsageClientAuth) != nil {
		return ""
	}
	return peerIdentity(state, pins)
}

// Revalidate on keep-alive requests too: a completed handshake does not prove
// that the peer's CA/leaf is still trusted or unexpired now.
func verifyCurrentPeer(state *tls.ConnectionState, roots *x509.CertPool, name string, usage x509.ExtKeyUsage) error {
	if state == nil || !state.HandshakeComplete || state.Version < tls.VersionTLS13 ||
		len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 || roots == nil {
		return ErrAuthorization
	}
	intermediates := x509.NewCertPool()
	for _, cert := range state.PeerCertificates[1:] {
		intermediates.AddCert(cert)
	}
	_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates,
		DNSName: name, KeyUsages: []x509.ExtKeyUsage{usage}})
	if err != nil {
		return ErrAuthorization
	}
	return nil
}

// Replace only the control HTTP pool on a credential/trust generation change.
// The public WSS/Guest tunnel is independent and survives a successful rotation.
// In-flight control requests are bounded by controlTimeout and are not replayed.
type reloadingControlTransport struct {
	mu         sync.Mutex
	files      tlsFiles
	name       string
	generation [32]byte
	transport  *http.Transport
	closed     bool
}

func (r *reloadingControlTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrAuthorization
	}
	cfg, generation, err := r.files.load(true)
	if err != nil {
		r.transport.CloseIdleConnections()
		r.mu.Unlock()
		return nil, ErrAuthorization
	}
	cfg.ServerName = r.name
	if r.generation != generation {
		previous := r.transport
		r.transport = newControlTransport(cfg)
		r.generation = generation
		previous.CloseIdleConnections()
	}
	transport := r.transport
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.closed || r.transport != transport {
			// A request that was already in flight at rotation must not leave
			// a retired pool alive when its response body is later released.
			transport.CloseIdleConnections()
		}
	}()
	response, err := transport.RoundTrip(req)
	if err != nil {
		return nil, ErrAuthorization
	}
	if verifyCurrentPeer(response.TLS, cfg.RootCAs, r.name, x509.ExtKeyUsageServerAuth) != nil {
		response.Body.Close()
		transport.CloseIdleConnections()
		return nil, ErrAuthorization
	}
	return response, nil
}

func (r *reloadingControlTransport) CloseIdleConnections() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.transport.CloseIdleConnections()
}
