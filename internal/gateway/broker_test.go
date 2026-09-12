package gateway

import (
	"net/netip"
	"strings"
	"testing"
)

func TestBrokerRouteConfiguration(t *testing.T) {
	peers := PeerPins{[32]byte{1}: "gw_testprimary"}
	prefixes := []netip.Prefix{netip.MustParsePrefix("10.31.0.0/24")}
	route, err := NewBrokerRoute("gw_testprimary", "https://gateway.example:8443/", prefixes, peers)
	if err != nil || route.ID() != "gw_testprimary" || route.URL() != "wss://gateway.example:8443/gateway/v1/rdp" {
		t.Fatal("valid route rejected", err)
	}
	prefixes[0] = netip.MustParsePrefix("192.168.0.0/16")
	if !route.Allows(netip.MustParseAddr("10.31.0.42")) || route.Allows(netip.MustParseAddr("192.168.0.42")) || route.Allows(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("route was mutated or private boundary lost")
	}
	for _, raw := range []string{"", "http://gateway.example", "wss://gateway.example", "https://u:p@gateway.example", "https://gateway.example?", "https://gateway.example?q=secret", "https://gateway.example/#secret", "https://gateway.example/api", "https://gateway.example/%2f", "https://gateway.example:", "https://gateway.example:0", "https://gateway.example:65536", "https://[fe80::1%25en0]"} {
		if _, err := NewBrokerRoute("gw_testprimary", raw, prefixes, peers); err == nil {
			t.Fatalf("invalid public origin accepted: %q", raw)
		}
	}
	for _, cidr := range []string{"0.0.0.0/0", "127.0.0.0/8", "10.0.0.1/24", "10.0.0.0/7", "::/0"} {
		if _, err := NewBrokerRoute("gw_testprimary", "https://gateway.example", []netip.Prefix{netip.MustParsePrefix(cidr)}, peers); err == nil {
			t.Fatal("unsafe target scope accepted", cidr)
		}
	}
	if _, err := NewBrokerRoute("gw_notregistered", "https://gateway.example", prefixes, peers); err == nil {
		t.Fatal("unregistered Gateway accepted")
	}
}

func TestBrokerRouteEnvironmentGate(t *testing.T) {
	keys := []string{"VC_WORKSPACE_NATIVE_GATEWAY_ID", "VC_WORKSPACE_NATIVE_GATEWAY_URL", "VC_WORKSPACE_NATIVE_GATEWAY_ALLOWED_TARGETS"}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	if route, err := LoadBrokerRoute(nil); err != nil || route != nil {
		t.Fatal("default route changed")
	}
	values := []string{"gw_testprimary", "https://gateway.example", "10.31.0.0/24"}
	control := &ControlListenerConfig{Peers: PeerPins{[32]byte{1}: values[0]}}
	for i, key := range keys {
		t.Setenv(key, values[i])
		if _, err := LoadBrokerRoute(nil); err == nil {
			t.Fatal("route allowed without dedicated listener")
		}
		if i < 2 {
			if _, err := LoadBrokerRoute(control); err == nil {
				t.Fatal("partial routing accepted")
			}
		}
	}
	if route, err := LoadBrokerRoute(control); err != nil || !strings.HasPrefix(route.URL(), "wss://") {
		t.Fatal("complete route rejected", err)
	}
}
