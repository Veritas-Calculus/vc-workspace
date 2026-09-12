package gateway

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/coder/websocket"
)

func macTLSFixture(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "macOS isolated gateway test CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "isolated gateway"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: leafKey}, caDER
}

type macFixtureAuthority struct {
	*testAuthority
	ticket string
}

func (a macFixtureAuthority) RedeemNativeGatewayTicket(ctx context.Context, id, ticket string) (store.NativeGatewayGrant, error) {
	if subtle.ConstantTimeCompare([]byte(ticket), []byte(a.ticket)) != 1 {
		return store.NativeGatewayGrant{}, ErrAuthorization
	}
	return a.testAuthority.RedeemNativeGatewayTicket(ctx, id, ticket)
}

// Swift launches this compiled test fixture through pipes. It never contacts a
// Guest or writes a CA/key to disk. Default go test skips it; EOF closes every
// listener/socket and emits only counters, never data or credentials.
func TestMacGatewayFixture(t *testing.T) {
	mode := os.Getenv("VC_WORKSPACE_MAC_GATEWAY_FIXTURE")
	if mode == "" {
		t.Skip("explicit macOS interop process required")
	}
	lifetime := 45 * time.Second
	if mode == "soak" {
		lifetime = 200 * time.Second
	}
	ctx, cancel := context.WithTimeout(t.Context(), lifetime)
	defer cancel()
	publicCert, ca := macTLSFixture(t)
	guestCert, _ := macTLSFixture(t)
	pin := sha256.Sum256(guestCert.Certificate[0])
	ticket := validTestTicket(t)
	authority := fixtureAuthority()
	grant := fixtureGrant()
	grant.CertificateSHA256 = pin[:]
	var consumed atomic.Bool
	var authorized atomic.Int64
	authority.redeem = func(context.Context) (store.NativeGatewayGrant, error) {
		if consumed.Swap(true) {
			return store.NativeGatewayGrant{}, ErrAuthorization
		}
		authorized.Add(1)
		return grant, nil
	}
	authority.renew = func(context.Context) (store.NativeGatewayGrant, error) {
		if mode == "revoke" {
			return store.NativeGatewayGrant{}, ErrAuthorization
		}
		return grant, nil
	}
	relay := fixtureRelay(t, macFixtureAuthority{testAuthority: authority, ticket: ticket})
	var bytesReceived atomic.Int64
	var guestTLS atomic.Bool
	var peerCompleted atomic.Bool
	var guestError atomic.Bool
	relay.dial = func(context.Context, string, string) (net.Conn, error) {
		upstream, guest := tcpPair(t)
		go func() {
			defer guest.Close()
			defer peerCompleted.Store(true)
			if mode != "native_pin" {
				n, _ := io.Copy(guest, guest)
				bytesReceived.Store(n)
				return
			}
			_ = guest.SetDeadline(time.Now().Add(15 * time.Second))
			var header [4]byte
			if _, err := io.ReadFull(guest, header[:]); err != nil {
				guestError.Store(true)
				return
			}
			length := binary.BigEndian.Uint16(header[2:])
			if header[0] != 3 || length < 11 || length > 2048 {
				guestError.Store(true)
				return
			}
			if _, err := io.CopyN(io.Discard, guest, int64(length)-4); err != nil {
				guestError.Store(true)
				return
			}
			_, err := guest.Write([]byte{3, 0, 0, 19, 14, 0xd0, 0, 0, 0, 0, 0, 2, 0, 8, 0, 1, 0, 0, 0})
			if err != nil {
				guestError.Store(true)
				return
			}
			secure := tls.Server(guest, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{guestCert}})
			if secure.Handshake() != nil {
				guestError.Store(true)
				return
			}
			guestTLS.Store(true)
			var payload [4096]byte
			n, _ := secure.Read(payload[:])
			bytesReceived.Store(int64(n))
		}()
		return upstream, nil
	}
	server := httptest.NewUnstartedServer(nil)
	transport, err := NewTransport(ctx, relay, server.Listener.Addr().String(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if mode == "echo" || mode == "soak" || mode == "revoke" || mode == "native_pin" {
			transport.ServeHTTP(w, r)
			return
		}
		if mode == "redirect" {
			http.Redirect(w, r, "https://"+r.Host+"/wrong-path", 302)
			return
		}
		protocol := TunnelSubprotocol
		if mode == "wrong_protocol" {
			protocol = "wrong"
		}
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol}})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		switch mode {
		case "stall":
			<-ctx.Done()
			return
		case "binary_ready":
			_ = ws.Write(ctx, websocket.MessageBinary, []byte("ready"))
		case "text", "oversized":
			_ = ws.Write(ctx, websocket.MessageText, []byte("ready"))
			if mode == "text" {
				_ = ws.Write(ctx, websocket.MessageText, []byte("private invalid message"))
			} else {
				_ = ws.Write(ctx, websocket.MessageBinary, make([]byte, 65537))
			}
		default:
			return
		}
		_, _, _ = ws.Read(ctx)
	})
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{publicCert}}
	server.StartTLS()
	defer server.Close()
	metadata := map[string]any{"url": strings.Replace(server.URL, "https:", "wss:", 1) + TunnelPath, "ca": ca, "certificate_sha256": hex.EncodeToString(pin[:]), "ticket": ticket}
	encoded, _ := json.Marshal(metadata)
	fmt.Printf("VCW_FIXTURE %s\n", encoded)
	stop := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(stop) }()
	select {
	case <-ctx.Done():
	case <-stop:
	}
	cancel()
	cleanup, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	if err := transport.Close(cleanup); err != nil {
		t.Error(err)
	}
	until := time.Now().Add(time.Second)
	for consumed.Load() && !peerCompleted.Load() && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	result, _ := json.Marshal(map[string]any{"requests": requests.Load(), "authorized": authorized.Load(), "closed": authority.closes.Load(), "bytes": bytesReceived.Load(), "guest_tls": guestTLS.Load(), "guest_error": guestError.Load(), "peer_closed": peerCompleted.Load()})
	fmt.Printf("VCW_RESULT %s\n", result)
}
