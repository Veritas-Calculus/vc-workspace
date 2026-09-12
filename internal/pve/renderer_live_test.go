package pve

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// This opt-in probe reads an already active user's RDP session. A PCI device
// or `direct rendering: Yes` alone is not evidence of hardware acceleration.
func TestLiveGuestDesktopRenderer(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_DESKTOP_RENDERER_AUDIT") != "true" {
		t.Skip("set VC_WORKSPACE_LIVE_DESKTOP_RENDERER_AUDIT=true to inspect the active Guest renderer")
	}
	vmid, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_DESKTOP_VMID"))
	username := strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_DESKTOP_USERNAME"))
	expected := strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_DESKTOP_EXPECTED_RENDERER"))
	accelerated := os.Getenv("VC_WORKSPACE_LIVE_DESKTOP_EXPECTED_ACCELERATED")
	if err != nil || vmid < 1 || username == "" || expected == "" || (accelerated != "yes" && accelerated != "no") {
		t.Fatal("explicit VMID, username, expected renderer and expected accelerated=yes|no are required")
	}
	client := liveClient(t)
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var machine VM
	for _, vm := range summary.VMs {
		if vm.VMID == vmid && vm.Kind == "qemu" && vm.Status == "running" {
			machine = vm
		}
	}
	if machine.VMID == 0 {
		t.Fatal("target is not a running QEMU guest")
	}
	const script = `set -eu
username=$1
uid=$(id -u -- "$username")
test "$uid" -ge 1000
session_pids=$(pgrep -u "$uid" -x xfce4-session)
set -- $session_pids
test "$#" -eq 1
display=$(tr '\0' '\n' < "/proc/$1/environ" | sed -n 's/^DISPLAY=//p' | head -n1)
xauth=$(tr '\0' '\n' < "/proc/$1/environ" | sed -n 's/^XAUTHORITY=//p' | head -n1)
test -n "$display"
printf 'session_uid=%s display=%s\n' "$uid" "$display"
runuser -u "$username" -- env DISPLAY="$display" XAUTHORITY="$xauth" timeout 15 glxinfo -B
`
	result, err := client.ExecGuest(t.Context(), machine.Node, vmid, []string{"/bin/sh", "-lc", script, "vc-workspace-renderer-probe", username})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("renderer probe failed (install mesa-utils in the test Guest): exit=%d stderr=%q", result.ExitCode, result.Stderr)
	}
	if !matchesDesktopRenderer(result.Stdout, expected, accelerated) {
		t.Fatalf("renderer did not match the expected mode: %s", result.Stdout)
	}
	t.Logf("VMID %d active RDP renderer:\n%s", vmid, result.Stdout)
}

func matchesDesktopRenderer(output, expected, accelerated string) bool {
	return strings.Contains(output, "OpenGL renderer string: "+expected) && strings.Contains(output, "Accelerated: "+accelerated)
}

func TestRendererProbeRejectsSoftwareFalsePositive(t *testing.T) {
	software := "direct rendering: Yes\nAccelerated: no\nOpenGL renderer string: llvmpipe (LLVM 19.1.7, 256 bits)"
	hardware := "direct rendering: Yes\nAccelerated: yes\nOpenGL renderer string: Mesa Intel(R) HD Graphics 530 (SKL GT2)"
	if matchesDesktopRenderer(software, "llvmpipe", "yes") || matchesDesktopRenderer(software, "Mesa Intel", "yes") ||
		!matchesDesktopRenderer(software, "llvmpipe", "no") || !matchesDesktopRenderer(hardware, "Mesa Intel", "yes") {
		t.Fatal("renderer audit must distinguish Mesa software rendering from hardware acceleration")
	}
}
