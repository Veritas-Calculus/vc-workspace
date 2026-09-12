package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

type testAuthority struct {
	redeem func(context.Context) (store.NativeGatewayGrant, error)
	renew  func(context.Context) (store.NativeGatewayGrant, error)
	close  func(context.Context) error
	issues atomic.Int64
	renews atomic.Int64
	closes atomic.Int64
}

func (a *testAuthority) RedeemNativeGatewayTicket(ctx context.Context, _, _ string) (store.NativeGatewayGrant, error) {
	a.issues.Add(1)
	return a.redeem(ctx)
}
func (a *testAuthority) RenewNativeGatewayGrant(ctx context.Context, _, _ string) (store.NativeGatewayGrant, error) {
	a.renews.Add(1)
	return a.renew(ctx)
}
func (a *testAuthority) CloseNativeGatewayGrant(ctx context.Context, _, _ string) error {
	a.closes.Add(1)
	if a.close != nil {
		return a.close(ctx)
	}
	return nil
}

func fixtureGrant() store.NativeGatewayGrant {
	return store.NativeGatewayGrant{ConnectionID: "conn_relay_fixture", GatewayID: "gw_testprimary", TunnelID: "tun_" + strings.Repeat("a", 32),
		Target: netip.MustParseAddr("10.31.0.110"), CertificateSHA256: make([]byte, 32), PolicyRevision: 1, ValidFor: 600 * time.Millisecond}
}

func fixtureAuthority() *testAuthority {
	return &testAuthority{redeem: func(context.Context) (store.NativeGatewayGrant, error) { return fixtureGrant(), nil },
		renew: func(context.Context) (store.NativeGatewayGrant, error) { return fixtureGrant(), nil }}
}

func fixtureRelay(t *testing.T, authority Authority) *Relay {
	t.Helper()
	relay, err := New(authority, Config{GatewayID: "gw_testprimary", AllowedTargets: []netip.Prefix{netip.MustParsePrefix("10.31.0.0/24")}, MaxConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	return relay
}

// Real loopback TCP sockets, not an in-memory stream. No VM or RDP endpoint is
// contacted. The unexported dial override maps the pinned fixture IP to this lab.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = listener.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	one, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = one.Close() })
	two, err := listener.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = two.Close() })
	return one, two
}

func startRelay(t *testing.T, relay *Relay, ticket string) (net.Conn, net.Conn, <-chan error, context.CancelFunc) {
	t.Helper()
	client, accepted := tcpPair(t)
	upstream, guest := tcpPair(t)
	relay.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp4" || address != "10.31.0.110:3389" {
			return nil, errors.New("dial escaped pinned endpoint")
		}
		return upstream, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- relay.Serve(ctx, accepted, ticket) }()
	return client, guest, done, cancel
}

func exchange(t *testing.T, from, to net.Conn, payload string) {
	t.Helper()
	if err := from.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(from, payload); err != nil {
		t.Fatal(err)
	}
	if err := to.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(to, got); err != nil || string(got) != payload {
		t.Fatal("opaque transport changed/lost payload", err)
	}
}

func awaitResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not terminate within bounded deadline")
		return nil
	}
}

func assertClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, err := conn.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("closed tunnel continued to forward bytes")
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		t.Fatal("socket remained open after lease/cancellation")
	}
}

func TestRelayBidirectionalRenewalAndCancellation(t *testing.T) {
	authority := fixtureAuthority()
	client, guest, done, cancel := startRelay(t, fixtureRelay(t, authority), "ticket is opaque to data plane")
	exchange(t, client, guest, "client RDP bytes")
	exchange(t, guest, client, "Guest RDP bytes")
	time.Sleep(800 * time.Millisecond) // outlive the original 600 ms grant
	exchange(t, client, guest, "after renewal")
	if authority.renews.Load() < 2 {
		t.Fatal("connection continued without lease renewal")
	}
	cancel()
	if err := awaitResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	assertClosed(t, client)
	assertClosed(t, guest)
	if authority.closes.Load() != 1 {
		t.Fatal("missing/duplicate closure acknowledgement")
	}
}

func TestRelayAuthorizationAndScopeFailBeforeDial(t *testing.T) {
	for _, reason := range []string{"denied", "outside_allowlist", "public_ip", "wrong_gateway", "missing_pin", "invalid_tunnel", "oversized_lease", "expired_in_rpc"} {
		t.Run(reason, func(t *testing.T) {
			authority := fixtureAuthority()
			authority.redeem = func(context.Context) (store.NativeGatewayGrant, error) {
				grant := fixtureGrant()
				switch reason {
				case "denied":
					return store.NativeGatewayGrant{}, errors.New("private control error with secret must not be exposed")
				case "outside_allowlist":
					grant.Target = netip.MustParseAddr("10.32.0.2")
				case "public_ip":
					grant.Target = netip.MustParseAddr("8.8.8.8")
				case "wrong_gateway":
					grant.GatewayID = "gw_othernode"
				case "missing_pin":
					grant.CertificateSHA256 = nil
				case "invalid_tunnel":
					grant.TunnelID = "bad"
				case "oversized_lease":
					grant.ValidFor = time.Hour
				case "expired_in_rpc":
					grant.ValidFor = 20 * time.Millisecond
					time.Sleep(40 * time.Millisecond)
				}
				return grant, nil
			}
			relay := fixtureRelay(t, authority)
			var dials atomic.Int64
			relay.dial = func(context.Context, string, string) (net.Conn, error) { dials.Add(1); return nil, ErrTransport }
			client, accepted := tcpPair(t)
			err := relay.Serve(t.Context(), accepted, "opaque")
			if (!errors.Is(err, ErrAuthorization) && !errors.Is(err, ErrLeaseExpired)) || dials.Load() != 0 || strings.Contains(err.Error(), "secret") {
				t.Fatal("invalid grant dialed a target or exposed an RPC error", err)
			}
			assertClosed(t, client)
		})
	}
}

func TestRelayRenewalFailureAndChangedScopeCloseBothSockets(t *testing.T) {
	for _, reason := range []string{"revoked", "control_partition", "changed_target", "changed_pin", "changed_policy", "changed_tunnel", "late_response"} {
		t.Run(reason, func(t *testing.T) {
			authority := fixtureAuthority()
			authority.renew = func(ctx context.Context) (store.NativeGatewayGrant, error) {
				grant := fixtureGrant()
				switch reason {
				case "revoked":
					return store.NativeGatewayGrant{}, store.ErrNotFound
				case "control_partition":
					<-ctx.Done()
					return store.NativeGatewayGrant{}, ctx.Err()
				case "changed_target":
					grant.Target = netip.MustParseAddr("10.31.0.111")
				case "changed_pin":
					grant.CertificateSHA256[0] = 1
				case "changed_policy":
					grant.PolicyRevision++
				case "changed_tunnel":
					grant.TunnelID = "tun_" + strings.Repeat("b", 32)
				case "late_response":
					<-ctx.Done()
					return grant, nil // an SDK must not revive a timed-out response
				}
				return grant, nil
			}
			started := time.Now()
			client, guest, done, _ := startRelay(t, fixtureRelay(t, authority), "opaque")
			exchange(t, client, guest, "authorized initial bytes")
			if err := awaitResult(t, done); err == nil {
				t.Fatal("failed renewal was accepted")
			}
			if time.Since(started) > 1500*time.Millisecond {
				t.Fatal("dead control plane extended data-plane lifetime")
			}
			assertClosed(t, client)
			assertClosed(t, guest)
			if authority.closes.Load() != 1 || authority.renews.Load() != 1 {
				t.Fatal("unexpected retries or missing closure")
			}
		})
	}
}

func TestRelayEOFAndCapacity(t *testing.T) {
	authority := fixtureAuthority()
	relay := fixtureRelay(t, authority)
	client, guest, done, _ := startRelay(t, relay, "first")
	exchange(t, client, guest, "ready")
	second, accepted := tcpPair(t)
	if err := relay.Serve(t.Context(), accepted, "second"); !errors.Is(err, ErrCapacity) || authority.issues.Load() != 1 {
		t.Fatal("capacity rejection consumed a ticket", err)
	}
	assertClosed(t, second)
	_ = guest.Close()
	if err := awaitResult(t, done); err != nil {
		t.Fatal(err)
	}
	assertClosed(t, client)
	if len(relay.slots) != 0 || authority.closes.Load() != 1 {
		t.Fatal("EOF leaked a capacity slot or grant")
	}
}

func TestRelayClosesTrafficBeforeFailedCleanup(t *testing.T) {
	authority := fixtureAuthority()
	closing := make(chan struct{})
	authority.close = func(ctx context.Context) error {
		close(closing)
		<-ctx.Done()
		return ctx.Err()
	}
	client, guest, done, cancel := startRelay(t, fixtureRelay(t, authority), "opaque")
	exchange(t, guest, client, "ready")
	cancel()
	select {
	case <-closing:
	case <-time.After(time.Second):
		t.Fatal("cleanup was not attempted")
	}
	assertClosed(t, client)
	assertClosed(t, guest)
	if err := awaitResult(t, done); !errors.Is(err, ErrClosure) || !errors.Is(err, context.Canceled) {
		t.Fatal("failed closure was hidden", err)
	}
}

func TestRelayConfigurationRequiresBoundedPrivateTargets(t *testing.T) {
	for _, prefix := range []string{"0.0.0.0/0", "10.0.0.0/7", "127.0.0.0/8", "169.254.0.0/16", "::/0", "::ffff:10.0.0.0/104", "10.31.0.110/24"} {
		_, err := New(fixtureAuthority(), Config{GatewayID: "gw_testprimary", AllowedTargets: []netip.Prefix{netip.MustParsePrefix(prefix)}, MaxConnections: 1})
		if !errors.Is(err, ErrConfiguration) {
			t.Fatal("unsafe target configuration", prefix, err)
		}
	}
	if _, err := New(fixtureAuthority(), Config{GatewayID: "gw_testprimary", MaxConnections: 1}); !errors.Is(err, ErrConfiguration) {
		t.Fatal("empty allowlist accepted")
	}
}

func TestRelayStalledDialCannotOutliveGrant(t *testing.T) {
	authority := fixtureAuthority()
	authority.redeem = func(context.Context) (store.NativeGatewayGrant, error) {
		grant := fixtureGrant()
		grant.ValidFor = 100 * time.Millisecond
		return grant, nil
	}
	relay := fixtureRelay(t, authority)
	relay.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	client, accepted := tcpPair(t)
	started := time.Now()
	if err := relay.Serve(t.Context(), accepted, "opaque"); !errors.Is(err, ErrTransport) || time.Since(started) > time.Second {
		t.Fatal("stalled Guest dial escaped the original grant", err)
	}
	assertClosed(t, client)
	if authority.closes.Load() != 1 || authority.renews.Load() != 0 {
		t.Fatal("undialed tunnel leaked or received renewal")
	}
}

func TestRelayBlockedWriterClosesOnControlPartition(t *testing.T) {
	authority := fixtureAuthority()
	authority.renew = func(ctx context.Context) (store.NativeGatewayGrant, error) {
		<-ctx.Done()
		return store.NativeGatewayGrant{}, ctx.Err()
	}
	client, accepted := tcpPair(t)
	upstream, guest := tcpPair(t)
	if err := upstream.(*net.TCPConn).SetWriteBuffer(1024); err != nil {
		t.Fatal(err)
	}
	if err := guest.(*net.TCPConn).SetReadBuffer(1024); err != nil {
		t.Fatal(err)
	}
	relay := fixtureRelay(t, authority)
	relay.dial = func(context.Context, string, string) (net.Conn, error) { return upstream, nil }
	done := make(chan error, 1)
	go func() { done <- relay.Serve(t.Context(), accepted, "opaque") }()
	written := make(chan struct{})
	go func() {
		defer close(written)
		buffer := make([]byte, 32*1024)
		for {
			if _, err := client.Write(buffer); err != nil {
				return
			}
		}
	}()
	// Never drain the Guest side: force the relay's writer to stall, then lose
	// control-plane reachability. Termination must still join both copy workers.
	if err := awaitResult(t, done); err == nil {
		t.Fatal("control partition left a blocked writer authorized")
	}
	_ = client.Close()
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("blocked sender was not released")
	}
	if authority.closes.Load() != 1 || len(relay.slots) != 0 {
		t.Fatal("blocked writer leaked its grant or capacity")
	}
}
