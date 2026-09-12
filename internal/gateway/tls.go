package gateway

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

// PeerPins maps a certificate's SHA-256 to one Gateway identity. A dedicated
// client CA is necessary but not sufficient: only these enrolled leafs may act.
type PeerPins map[[32]byte]string

func ParsePeerPins(data []byte) (PeerPins, error) {
	var configured map[string][]string
	if len(data) > 64*1024 || !uniqueObject(data) || json.Unmarshal(data, &configured) != nil || len(configured) == 0 || len(configured) > 128 {
		return nil, ErrConfiguration
	}
	pins := PeerPins{}
	for id, list := range configured {
		if !gatewayID.MatchString(id) || len(list) == 0 || len(list) > 2 {
			return nil, ErrConfiguration
		}
		for _, text := range list {
			decoded, err := hex.DecodeString(text)
			if err != nil || len(decoded) != 32 || text != strings.ToLower(text) {
				return nil, ErrConfiguration
			}
			key := [32]byte(decoded)
			if _, exists := pins[key]; exists {
				return nil, ErrConfiguration
			}
			pins[key] = id
		}
	}
	return pins, nil
}

func ReadConfigFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrConfiguration
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		return nil, ErrConfiguration
	}
	return data, nil
}

func LoadCertificate(certFile, keyFile string) (tls.Certificate, error) {
	cert, err := ReadConfigFile(certFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	key, err := ReadConfigFile(keyFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	defer clear(key)
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return tls.Certificate{}, ErrConfiguration
	}
	return pair, nil
}

func LoadCA(path string) (*x509.CertPool, error) {
	data, err := ReadConfigFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, ErrConfiguration
	}
	return pool, nil
}

func ControlServerTLS(cert tls.Certificate, ca *x509.CertPool) (*tls.Config, error) {
	if len(cert.Certificate) == 0 || cert.PrivateKey == nil || ca == nil || len(ca.Subjects()) == 0 {
		return nil, ErrConfiguration
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: ca,
		ClientAuth: tls.RequireAndVerifyClientCert, SessionTicketsDisabled: true}, nil
}

func peerIdentity(state *tls.ConnectionState, pins PeerPins) string {
	if state == nil || !state.HandshakeComplete || state.Version < tls.VersionTLS13 || len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
		return ""
	}
	leaf := state.PeerCertificates[0]
	// Certificates on an idle keep-alive connection can expire after handshake.
	if time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return ""
	}
	return pins[sha256.Sum256(leaf.Raw)]
}

func uniqueObject(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return false
	}
	keys := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || keys[key] {
			return false
		}
		keys[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}
