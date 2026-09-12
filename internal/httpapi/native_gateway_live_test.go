package httpapi

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/Veritas-Calculus/vc-workspace/internal/guestdesktop"
)

// Read-only certificate discovery through the production PVE/QGA adapter on
// the one isolated Linux acceptance VM. No OS login, restart, account write,
// certificate replacement, or business VM access is performed by this test.
func TestLiveGatewayGuestCertificate(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_GATEWAY_CERTIFICATE_AUDIT") != "true" {
		t.Skip("explicit isolated Gateway certificate acceptance required")
	}
	if os.Getenv("VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID") != "160" {
		t.Fatal("isolated VM 160 must be explicit")
	}
	client := livePrivilegeClient(t)
	machine := liveMachine(t, client, 160)
	if machine.Node != "infra-node1" || machine.Status != "running" || machine.Template || !isManagedDesktop(machine) || !strings.HasSuffix(machine.Name, "-check") {
		t.Fatal("isolated running acceptance Guest required")
	}
	read := func(script string) string {
		t.Helper()
		result, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, []string{"/bin/sh", "-c", script})
		if err != nil || result.ExitCode != 0 {
			t.Fatal("read-only Guest certificate audit failed")
		}
		return strings.TrimSpace(result.Stdout)
	}
	const baseline = `set -eu; test "$(. /etc/os-release; echo $VERSION_ID)" = 13; test -z "$(pgrep -x Xorg || true)"; sha256sum /etc/xrdp/xrdp.ini /etc/xrdp/sesman.ini /etc/xrdp/cert.pem; getent passwd | sha256sum`
	before := read(baseline)
	pin, err := guestdesktop.RDPCertificate(t.Context(), client, machine, "linux")
	if err != nil {
		t.Fatal(err)
	}
	configured := read("openssl x509 -in /etc/xrdp/cert.pem -noout -fingerprint -sha256")
	_, fingerprint, ok := strings.Cut(configured, "=")
	fingerprint = strings.ToLower(strings.ReplaceAll(fingerprint, ":", ""))
	if !ok || fingerprint != hex.EncodeToString(pin[:]) {
		t.Fatal("actual TLS listener does not match independently inspected fixture certificate")
	}
	if after := read(baseline); after != before {
		t.Fatal("certificate observation changed Guest configuration or accounts")
	}
	t.Logf("VM 160 production QGA loopback RDP/TLS discovery matched leaf %x; no OS authentication or account/config changes", pin)
}
