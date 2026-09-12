package gateway

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
)

func TestWebSocketPostgresMTLSRevocationAndReplay(t *testing.T) {
	db, dsn, digest, ticket := postgresRelayFixture(t)
	_, authority, _, _ := realControlTLS(t, db)
	relay := fixtureRelay(t, authority)
	relay.slots = make(chan struct{}, 2) // allow replay to reach the one-time DB gate
	upstream, guest := tcpPair(t)
	var dials atomic.Int64
	relay.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp4" || address != "10.31.0.110:3389" || dials.Add(1) != 1 {
			return nil, ErrTransport
		}
		return upstream, nil
	}
	server, transport, client := realPublicTLS(t, relay, authority.Ping)
	res, err := client.Get(server.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatal("real DB/mTLS readiness failed")
	}
	ws := dialWebSocket(t, server, client)
	stream := authorizeWebSocket(t, ws, ticket.Token)
	exchange(t, stream, guest, "PostgreSQL through mTLS through WSS")
	exchange(t, guest, stream, "opaque Guest stream to TLS client")
	replay := dialWebSocket(t, server, client)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err = replay.Write(ctx, websocket.MessageText, []byte(ticket.Token)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = replay.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatal("spent ticket replay was not rejected before ready", err)
	}
	if dials.Load() != 1 {
		t.Fatal("replay made another Guest connection")
	}
	if err = db.RevokeNativeSession(t.Context(), digest[:]); err != nil {
		t.Fatal(err)
	}
	// Read the actual encrypted transport, not a mocked connection state. Its
	// lifetime must remain bounded even with TLS handshake/HTTP/DB overhead.
	deadline, stop := context.WithTimeout(t.Context(), store.GatewayAuthorizationWindow+500*time.Millisecond)
	defer stop()
	if _, _, err = ws.Read(deadline); err == nil || deadline.Err() != nil {
		t.Fatal("Native logout failed to close WSS within the current lease", err)
	}
	assertClosed(t, guest)
	shutdown, stopShutdown := context.WithTimeout(t.Context(), 2*time.Second)
	defer stopShutdown()
	if err = transport.Close(shutdown); err != nil {
		t.Fatal(err)
	}
	check, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close(t.Context())
	var closed bool
	var audits int
	if err = check.QueryRow(t.Context(), `SELECT closed_at IS NOT NULL FROM gateway_session_tickets`).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if err = check.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE event_type LIKE 'gateway.%'`).Scan(&audits); err != nil || !closed || audits != 3 {
		t.Fatal("WSS/mTLS lifecycle audit did not converge exactly once", err, closed, audits)
	}
}
