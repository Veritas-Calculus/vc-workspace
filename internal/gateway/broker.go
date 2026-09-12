package gateway

import (
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// BrokerRoute is constructed from deployment configuration, never from an API
// request. Its fields remain private so handlers cannot receive unchecked URLs
// or bypass the same private-CIDR boundary enforced by the relay.
type BrokerRoute struct {
	id      string
	url     string
	allowed []netip.Prefix
}

func NewBrokerRoute(id, publicURL string, allowed []netip.Prefix, peers PeerPins) (*BrokerRoute, error) {
	enrolled := false
	for _, peerID := range peers {
		enrolled = enrolled || peerID == id
	}
	u, err := url.Parse(publicURL)
	if err != nil || !enrolled || !gatewayID.MatchString(id) || !validTargetPrefixes(allowed) ||
		u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" ||
		(u.Path != "" && u.Path != "/") || strings.Contains(u.Host, "%") || strings.HasSuffix(u.Host, ":") {
		return nil, ErrConfiguration
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, ErrConfiguration
		}
	}
	u.Scheme, u.Path = "wss", TunnelPath
	return &BrokerRoute{id: id, url: u.String(), allowed: append([]netip.Prefix(nil), allowed...)}, nil
}

func (r *BrokerRoute) ID() string  { return r.id }
func (r *BrokerRoute) URL() string { return r.url }
func (r *BrokerRoute) Allows(target netip.Addr) bool {
	if r == nil || !target.Is4() || !target.IsPrivate() {
		return false
	}
	for _, prefix := range r.allowed {
		if prefix.Contains(target) {
			return true
		}
	}
	return false
}

// Routing the Native endpoint through a Gateway is a distinct opt-in from
// merely running the control listener. Once configured it is mandatory for
// that endpoint: errors and old clients must not fall back to direct RDP.
func LoadBrokerRoute(control *ControlListenerConfig) (*BrokerRoute, error) {
	id := os.Getenv("VC_WORKSPACE_NATIVE_GATEWAY_ID")
	public := os.Getenv("VC_WORKSPACE_NATIVE_GATEWAY_URL")
	targets := os.Getenv("VC_WORKSPACE_NATIVE_GATEWAY_ALLOWED_TARGETS")
	if id == "" && public == "" && targets == "" {
		return nil, nil
	}
	if control == nil {
		return nil, ErrConfiguration
	}
	var allowed []netip.Prefix
	for _, raw := range strings.Split(targets, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return nil, ErrConfiguration
		}
		allowed = append(allowed, prefix)
	}
	return NewBrokerRoute(id, public, allowed, control.Peers)
}
