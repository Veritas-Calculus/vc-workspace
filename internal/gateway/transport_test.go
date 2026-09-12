package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/coder/websocket"
)

func TestTransportCloseReasonNeverEchoesPeerErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "eof"}, {ErrAuthorization, "authorization"}, {ErrLeaseExpired, "lease_expired"},
		{ErrTransport, "transport"}, {ErrCapacity, "capacity"}, {context.Canceled, "shutdown"},
		{errors.New("secret ticket peer bytes"), "internal"},
		{errors.Join(ErrAuthorization, errors.New("private RPC")), "authorization"},
	} {
		if got := transportCloseReason(tc.err); got != tc.want {
			t.Fatalf("reason = %q, want %q", got, tc.want)
		}
	}
}

func realPublicTLS(t *testing.T, relay *Relay, ready func(context.Context) error) (*httptest.Server, *Transport, *http.Client) {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	transport, err := NewTransport(t.Context(), relay, server.Listener.Addr().String(), ready)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = transport
	ca := newTLSCA(t)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{ca.issue(t, false, time.Now().Add(time.Hour))}}
	server.StartTLS()
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := transport.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	tlsTransport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: ca.pool}}
	t.Cleanup(tlsTransport.CloseIdleConnections)
	return server, transport, &http.Client{Transport: tlsTransport, Timeout: 2 * time.Second}
}

func dialWebSocket(t *testing.T, server *httptest.Server, client *http.Client) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "https:", "wss:", 1)+TunnelPath,
		&websocket.DialOptions{HTTPClient: client, Subprotocols: []string{TunnelSubprotocol}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func authorizeWebSocket(t *testing.T, ws *websocket.Conn, ticket string) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageText, []byte(ticket)); err != nil {
		t.Fatal(err)
	}
	typ, data, err := ws.Read(ctx)
	if err != nil || typ != websocket.MessageText || string(data) != "ready" {
		t.Fatal("Gateway did not acknowledge successful authorization and Guest dial", err)
	}
	stream := websocket.NetConn(t.Context(), ws, websocket.MessageBinary)
	ws.SetReadLimit(MaxFrameBytes)
	return &immediateWebSocketConn{Conn: stream, websocket: ws}
}

func TestWebSocketTLSBinaryRelayAndHijackedShutdown(t *testing.T) {
	authority := fixtureAuthority()
	relay := fixtureRelay(t, authority)
	upstream, guest := tcpPair(t)
	var dials atomic.Int64
	relay.dial = func(context.Context, string, string) (net.Conn, error) { dials.Add(1); return upstream, nil }
	server, transport, client := realPublicTLS(t, relay, func(context.Context) error { return nil })
	ws := dialWebSocket(t, server, client)
	if dials.Load() != 0 || authority.issues.Load() != 0 {
		t.Fatal("HTTP upgrade authorized or dialed before receiving the ticket")
	}
	stream := authorizeWebSocket(t, ws, validTestTicket(t))
	exchange(t, stream, guest, "TLS/WebSocket to Guest")
	exchange(t, guest, stream, "Guest to TLS/WebSocket")
	if ws.Subprotocol() != TunnelSubprotocol || dials.Load() != 1 {
		t.Fatal("wrong negotiated protocol or duplicate dial")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := transport.Close(ctx); err != nil {
		t.Fatal("shutdown did not join hijacked transport", err)
	}
	assertClosed(t, guest)
	if _, err := stream.Read(make([]byte, 1)); err == nil {
		t.Fatal("WebSocket stayed open after service shutdown")
	}
	if authority.closes.Load() != 1 || len(transport.slots) != 0 {
		t.Fatal("shutdown leaked connection/grant")
	}
}

func TestWebSocketRejectsUnsafeHTTPBeforeTicketConsumption(t *testing.T) {
	authority := fixtureAuthority()
	server, _, client := realPublicTLS(t, fixtureRelay(t, authority), func(context.Context) error { return nil })
	for _, reason := range []string{"origin", "empty_origin", "cookie", "authorization", "query_ticket", "encoded_path", "wrong_host", "wrong_protocol", "missing_protocol", "post"} {
		t.Run(reason, func(t *testing.T) {
			path := TunnelPath
			method := http.MethodGet
			if reason == "query_ticket" {
				path += "?ticket=" + validTestTicket(t)
			}
			if reason == "encoded_path" {
				path = "/gateway/v1/%72dp"
			}
			if reason == "post" {
				method = http.MethodPost
			}
			req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
			req.Header.Set("Sec-WebSocket-Version", "13")
			req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			req.Header.Set("Sec-WebSocket-Protocol", TunnelSubprotocol)
			switch reason {
			case "origin", "empty_origin":
				req.Header["Origin"] = []string{""}
				if reason == "origin" {
					req.Header.Set("Origin", server.URL)
				}
			case "cookie":
				req.Header.Set("Cookie", "session=ignored")
			case "authorization":
				req.Header.Set("Authorization", "Bearer ignored")
			case "wrong_host":
				req.Host = "attacker.invalid"
			case "wrong_protocol":
				req.Header.Set("Sec-WebSocket-Protocol", "another-protocol")
			case "missing_protocol":
				req.Header.Del("Sec-WebSocket-Protocol")
			}
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != http.StatusBadRequest || authority.issues.Load() != 0 {
				t.Fatal("unsafe WebSocket HTTP request reached authorization", res.StatusCode)
			}
		})
	}
}

func TestWebSocketFirstFrameIsBoundedAndNeverForwarded(t *testing.T) {
	for _, reason := range []string{"binary_ticket", "oversized", "noncanonical", "missing"} {
		t.Run(reason, func(t *testing.T) {
			authority := fixtureAuthority()
			relay := fixtureRelay(t, authority)
			var dials atomic.Int64
			relay.dial = func(context.Context, string, string) (net.Conn, error) { dials.Add(1); return nil, ErrTransport }
			server, _, client := realPublicTLS(t, relay, func(context.Context) error { return nil })
			ws := dialWebSocket(t, server, client)
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			data := validTestTicket(t)
			typ := websocket.MessageText
			switch reason {
			case "binary_ticket":
				typ = websocket.MessageBinary
			case "oversized":
				data = strings.Repeat("x", 1024)
			case "noncanonical":
				data = "gwt_" + strings.Repeat("A", 42) + "B"
			}
			if reason != "missing" {
				_ = ws.Write(ctx, typ, []byte(data))
			}
			if _, _, err := ws.Read(ctx); err == nil || ctx.Err() != nil {
				t.Fatal("unauthorized WebSocket did not close within its first-frame deadline", err)
			}
			if authority.issues.Load() != 0 || dials.Load() != 0 {
				t.Fatal("invalid first frame was forwarded or consumed")
			}
		})
	}
}

func TestWebSocketControlPartitionClosesWithoutPeerCloseHandshake(t *testing.T) {
	authority := fixtureAuthority()
	authority.renew = func(ctx context.Context) (store.NativeGatewayGrant, error) {
		<-ctx.Done()
		return store.NativeGatewayGrant{}, ctx.Err()
	}
	relay := fixtureRelay(t, authority)
	upstream, guest := tcpPair(t)
	relay.dial = func(context.Context, string, string) (net.Conn, error) { return upstream, nil }
	server, _, client := realPublicTLS(t, relay, func(context.Context) error { return nil })
	ws := dialWebSocket(t, server, client)
	stream := authorizeWebSocket(t, ws, validTestTicket(t))
	exchange(t, stream, guest, "ready to revoke")
	// Do not read/pong/ack any WebSocket control frames while waiting for the
	// Guest socket to close. A graceful WS close would exceed the lease here.
	started := time.Now()
	_ = guest.SetReadDeadline(started.Add(1500 * time.Millisecond))
	_, err := guest.Read(make([]byte, 1))
	var network net.Error
	if err == nil || (errors.As(err, &network) && network.Timeout()) {
		t.Fatal("uncooperative WebSocket peer delayed Guest socket closure", err)
	}
	if _, err = stream.Read(make([]byte, 1)); err == nil {
		t.Fatal("expired tunnel remained readable")
	}
}

func TestWebSocketOversizedDataAndReadinessFailure(t *testing.T) {
	authority := fixtureAuthority()
	relay := fixtureRelay(t, authority)
	upstream, guest := tcpPair(t)
	relay.dial = func(context.Context, string, string) (net.Conn, error) { return upstream, nil }
	server, _, client := realPublicTLS(t, relay, func(context.Context) error { return ErrAuthorization })
	res, err := client.Get(server.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatal("unavailable authority reported ready")
	}
	ws := dialWebSocket(t, server, client)
	_ = authorizeWebSocket(t, ws, validTestTicket(t))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go func() { _, _ = io.Copy(io.Discard, guest) }()
	_ = ws.Write(ctx, websocket.MessageBinary, make([]byte, MaxFrameBytes+1))
	if _, _, err := ws.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatal("oversized data frame was not closed", err)
	}
}
