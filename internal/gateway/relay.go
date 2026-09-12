// Package gateway implements a bounded, lease-controlled data plane and opt-in
// TLS services. Its relay core also accepts an already authenticated transport.
package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"regexp"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

var (
	ErrConfiguration = errors.New("invalid Gateway configuration")
	ErrCapacity      = errors.New("Gateway capacity exhausted")
	ErrAuthorization = errors.New("Gateway authorization failed")
	ErrLeaseExpired  = errors.New("Gateway lease expired")
	ErrTransport     = errors.New("Gateway transport failed")
	ErrClosure       = errors.New("Gateway closure acknowledgement failed")
)

const controlTimeout = 2 * time.Second

var gatewayID = regexp.MustCompile(`^gw_[a-z0-9_-]{8,64}$`)
var tunnelID = regexp.MustCompile(`^tun_[A-Za-z0-9_-]{24,128}$`)

// Implementations must honor context deadlines and authenticate this Gateway's
// service identity. Neither target addresses nor Gateway IDs come from clients.
type Authority interface {
	RedeemNativeGatewayTicket(context.Context, string, string) (store.NativeGatewayGrant, error)
	RenewNativeGatewayGrant(context.Context, string, string) (store.NativeGatewayGrant, error)
	CloseNativeGatewayGrant(context.Context, string, string) error
}

type Config struct {
	GatewayID      string
	AllowedTargets []netip.Prefix
	MaxConnections int
}

type Relay struct {
	authority Authority
	id        string
	allowed   []netip.Prefix
	slots     chan struct{}
	dial      func(context.Context, string, string) (net.Conn, error)
}

func New(authority Authority, config Config) (*Relay, error) {
	if authority == nil || !gatewayID.MatchString(config.GatewayID) || !validTargetPrefixes(config.AllowedTargets) ||
		config.MaxConnections < 1 || config.MaxConnections > 4096 {
		return nil, ErrConfiguration
	}
	return &Relay{authority: authority, id: config.GatewayID, allowed: append([]netip.Prefix(nil), config.AllowedTargets...),
		slots: make(chan struct{}, config.MaxConnections), dial: (&net.Dialer{Timeout: controlTimeout, KeepAlive: 30 * time.Second}).DialContext}, nil
}

func validTargetPrefixes(prefixes []netip.Prefix) bool {
	if len(prefixes) == 0 || len(prefixes) > 128 {
		return false
	}
	private := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16")}
	for _, prefix := range prefixes {
		valid := false
		for _, scope := range private {
			valid = valid || (prefix.IsValid() && prefix.Addr().Is4() && prefix == prefix.Masked() &&
				prefix.Bits() >= scope.Bits() && scope.Contains(prefix.Addr()))
		}
		if !valid {
			return false
		}
	}
	return true
}

func (r *Relay) validGrant(grant store.NativeGatewayGrant) bool {
	if grant.GatewayID != r.id || !tunnelID.MatchString(grant.TunnelID) || grant.ConnectionID == "" || len(grant.ConnectionID) > 140 ||
		!grant.Target.Is4() || !grant.Target.IsPrivate() || len(grant.CertificateSHA256) != 32 || grant.PolicyRevision <= 0 ||
		grant.ValidFor <= 0 || grant.ValidFor > store.GatewayAuthorizationWindow {
		return false
	}
	for _, prefix := range r.allowed {
		if prefix.Contains(grant.Target) {
			return true
		}
	}
	return false
}

// Serve owns client, including on rejection. Only committed, unexpired authority
// can cause a dial to the pinned literal IP on TCP/3389. Bytes are opaque: Guest
// RDP TLS and certificate verification remain end-to-end at the native client.
// Never wrap returned errors with tickets, credentials, or control RPC bodies.
func (r *Relay) Serve(ctx context.Context, client net.Conn, ticket string) (result error) {
	return r.serve(ctx, client, ticket, nil)
}

func (r *Relay) serve(ctx context.Context, client net.Conn, ticket string, ready func(context.Context) error) (result error) {
	if client == nil {
		return ErrTransport
	}
	defer client.Close()
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	default:
		return ErrCapacity
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	started := time.Now()
	rpc, cancel := context.WithTimeout(ctx, controlTimeout)
	grant, err := r.authority.RedeemNativeGatewayTicket(rpc, r.id, ticket)
	rpcErr := rpc.Err()
	cancel()
	// A known consumed ticket is closed even if its grant is malformed/late.
	// An uncertain redemption has no usable ID; its DB lease expires itself.
	if grant.GatewayID == r.id && tunnelID.MatchString(grant.TunnelID) {
		defer func() {
			_ = client.Close()
			closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if r.authority.CloseNativeGatewayGrant(closeCtx, r.id, grant.TunnelID) != nil {
				result = errors.Join(result, ErrClosure)
			}
		}()
	}
	if err != nil || rpcErr != nil || !r.validGrant(grant) {
		return ErrAuthorization
	}
	deadline := started.Add(grant.ValidFor) // monotonic, includes RPC/commit time
	if !time.Now().Before(deadline) {
		return ErrLeaseExpired
	}
	dialCtx, stopDial := context.WithDeadline(ctx, deadline)
	guest, err := r.dial(dialCtx, "tcp4", net.JoinHostPort(grant.Target.String(), "3389"))
	dialErr := dialCtx.Err()
	stopDial()
	if guest != nil {
		defer guest.Close()
	}
	if err != nil || dialErr != nil || guest == nil {
		return ErrTransport
	}
	if !time.Now().Before(deadline) {
		return ErrLeaseExpired
	}
	// Both OS socket deadlines and the independent lease timer enforce expiry,
	// including blocked writes, idle sockets, or a stalled renewal RPC.
	if client.SetDeadline(deadline) != nil || guest.SetDeadline(deadline) != nil {
		return ErrTransport
	}
	if ready != nil {
		readyCtx, cancel := context.WithDeadline(ctx, deadline)
		err := ready(readyCtx)
		cancel()
		if err != nil || !time.Now().Before(deadline) {
			return ErrTransport
		}
	}
	controlCtx, stopControl := context.WithCancel(ctx)
	defer stopControl()
	done := make(chan error, 2)
	go func() { _, err := io.Copy(guest, client); done <- err }()
	go func() { _, err := io.Copy(client, guest); done <- err }()
	defer func() {
		// Stop traffic before waiting for cleanup RPCs. Join both copy workers.
		stopControl()
		_ = client.Close()
		_ = guest.Close()
		<-done
		<-done
	}()
	expiry := time.NewTimer(time.Until(deadline))
	defer expiry.Stop()
	renew := time.NewTimer(time.Until(deadline) / 2)
	defer renew.Stop()
	type renewal struct {
		grant   store.NativeGatewayGrant
		started time.Time
		err     error
	}
	replies := make(chan renewal, 1)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-expiry.C:
			return ErrLeaseExpired
		case err := <-done:
			// Keep both terminal results available for the deferred worker join.
			done <- err
			if err != nil {
				return ErrTransport
			}
			return nil
		case <-renew.C:
			requestStart := time.Now()
			rpcDeadline := minTime(deadline, requestStart.Add(controlTimeout))
			go func() {
				rpc, stop := context.WithDeadline(controlCtx, rpcDeadline)
				defer stop()
				next, err := r.authority.RenewNativeGatewayGrant(rpc, r.id, grant.TunnelID)
				if rpc.Err() != nil {
					err = ErrAuthorization
				}
				replies <- renewal{next, requestStart, err}
			}()
		case reply := <-replies:
			next := reply.grant
			nextDeadline := reply.started.Add(next.ValidFor)
			if reply.err != nil || !r.validGrant(next) || next.TunnelID != grant.TunnelID || next.ConnectionID != grant.ConnectionID ||
				next.Target != grant.Target || next.PolicyRevision != grant.PolicyRevision || !bytes.Equal(next.CertificateSHA256, grant.CertificateSHA256) {
				return ErrAuthorization
			}
			if !time.Now().Before(deadline) || !time.Now().Before(nextDeadline) {
				return ErrLeaseExpired
			}
			deadline = nextDeadline
			if client.SetDeadline(deadline) != nil || guest.SetDeadline(deadline) != nil {
				return ErrTransport
			}
			expiry.Reset(time.Until(deadline))
			renew.Reset(time.Until(deadline) / 2)
		}
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
