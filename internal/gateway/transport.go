package gateway

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	TunnelPath        = "/gateway/v1/rdp"
	TunnelSubprotocol = "vc-workspace-rdp.v1"
	MaxFrameBytes     = 64 * 1024
)

type Transport struct {
	relay   *Relay
	host    string
	ready   func(context.Context) error
	ctx     context.Context
	cancel  context.CancelFunc
	slots   chan struct{}
	mu      sync.Mutex
	closing bool
	workers sync.WaitGroup
	logger  *slog.Logger
}

func NewTransport(parent context.Context, relay *Relay, host string, ready func(context.Context) error) (*Transport, error) {
	if relay == nil || host == "" || len(host) > 255 || strings.ContainsAny(host, "/\\@?# \r\n\t") || ready == nil {
		return nil, ErrConfiguration
	}
	ctx, cancel := context.WithCancel(parent)
	return &Transport{relay: relay, host: host, ready: ready, ctx: ctx, cancel: cancel, slots: make(chan struct{}, cap(relay.slots)), logger: slog.Default()}, nil
}

// Close stops and joins hijacked connections, which http.Server.Shutdown does
// not own. No new handler can Add to workers after the closing flag is set.
func (t *Transport) Close(ctx context.Context) error {
	t.mu.Lock()
	t.closing = true
	t.cancel()
	t.mu.Unlock()
	done := make(chan struct{})
	go func() { t.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *Transport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.TLS == nil || !r.TLS.HandshakeComplete || r.TLS.Version < tls.VersionTLS13 || r.Host != t.host ||
		r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		http.Error(w, "Invalid Gateway transport", http.StatusBadRequest)
		return
	}
	if r.URL.Path == "/health" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.URL.Path == "/ready" {
		ctx, cancel := context.WithTimeout(t.ctx, controlTimeout)
		defer cancel()
		if t.ctx.Err() != nil || t.ready(ctx) != nil {
			http.Error(w, "Gateway unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// This protocol is for native clients. Browser Origin, cookies and bearer
	// headers are rejected; the sole ticket travels in the bounded first frame.
	if r.URL.Path != TunnelPath || len(r.Header.Values("Origin")) != 0 || len(r.Header.Values("Cookie")) != 0 ||
		len(r.Header.Values("Authorization")) != 0 || len(r.Header.Values("Sec-WebSocket-Protocol")) != 1 || r.Header.Get("Sec-WebSocket-Protocol") != TunnelSubprotocol {
		http.Error(w, "Gateway protocol rejected", http.StatusBadRequest)
		return
	}
	t.mu.Lock()
	if t.closing || t.ctx.Err() != nil {
		t.mu.Unlock()
		http.Error(w, "Gateway unavailable", http.StatusServiceUnavailable)
		return
	}
	select {
	case t.slots <- struct{}{}:
		t.workers.Add(1)
	default:
		t.mu.Unlock()
		http.Error(w, "Gateway capacity exhausted", http.StatusServiceUnavailable)
		return
	}
	t.mu.Unlock()
	defer func() { <-t.slots; t.workers.Done() }()
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{TunnelSubprotocol}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(t.ctx)
	defer cancel()
	initial, stopInitial := context.WithTimeout(ctx, 3*time.Second)
	c.SetReadLimit(47)
	kind, bytes, err := c.Read(initial)
	stopInitial()
	if err != nil || kind != websocket.MessageText || !validTicket(string(bytes)) {
		clear(bytes)
		return
	}
	ticket := string(bytes)
	clear(bytes)
	stream := websocket.NetConn(ctx, c, websocket.MessageBinary)
	c.SetReadLimit(MaxFrameBytes) // NetConn disables this by default; restore it.
	client := &immediateWebSocketConn{Conn: stream, websocket: c}
	defer client.Close()
	// 101 only upgrades transport. "ready" is sent after committed redemption,
	// endpoint validation and Guest TCP dial, before any binary RDP traffic.
	err = t.relay.serve(ctx, client, ticket, func(readyCtx context.Context) error {
		return c.Write(readyCtx, websocket.MessageText, []byte("ready"))
	})
	// Never include raw transport/RPC errors or opaque ticket/peer bytes.
	t.logger.Info("Gateway tunnel finished", "reason", transportCloseReason(err))
	if errors.Is(err, ErrClosure) {
		t.logger.Warn("Gateway transport closed without authority acknowledgement")
	}
}

func transportCloseReason(err error) string {
	switch {
	case err == nil:
		return "eof"
	case errors.Is(err, ErrAuthorization):
		return "authorization"
	case errors.Is(err, ErrLeaseExpired):
		return "lease_expired"
	case errors.Is(err, ErrTransport):
		return "transport"
	case errors.Is(err, ErrCapacity):
		return "capacity"
	case errors.Is(err, context.Canceled):
		return "shutdown"
	default:
		return "internal"
	}
}

type immediateWebSocketConn struct {
	net.Conn
	websocket *websocket.Conn
}

func (c *immediateWebSocketConn) Close() error {
	err := c.websocket.CloseNow() // never wait for a peer's close handshake
	_ = c.Conn.Close()            // cancel NetConn contexts and deadline timers
	return err
}

func validTicket(ticket string) bool {
	if len(ticket) != 47 || !strings.HasPrefix(ticket, "gwt_") {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(ticket[4:])
	return err == nil && len(decoded) == 32
}
