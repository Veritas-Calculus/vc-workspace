package pve

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Only bounded child processes: no file/account/configuration writes, existing
// process targets or power-state changes. The signal probe terminates itself.
func TestLiveLinuxGuestExecCompletion(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_GUEST_EXEC_STATUS_AUDIT") != "true" {
		t.Skip("explicit Linux Guest execution status audit")
	}
	vmid, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_DESKTOP_VMID"))
	if err != nil || vmid <= 0 {
		t.Fatal("explicit positive Guest VMID required")
	}
	client := liveClient(t)
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var guest VM
	for _, machine := range summary.VMs {
		if machine.VMID == vmid {
			guest = machine
		}
	}
	if guest.Kind != "qemu" || guest.Template || guest.Status != "running" {
		t.Fatal("running non-template Guest required")
	}
	config, err := client.VMConfiguration(t.Context(), guest.Node, vmid)
	if err != nil || config.OSType != "l26" {
		t.Fatal("Linux Guest required", err)
	}
	for _, test := range []struct {
		script          string
		exit            int
		output, failure string
	}{
		{"printf 'vcw-guest-exec-proof\\n'", 0, "vcw-guest-exec-proof\n", ""},
		{"exit 37", 37, "", ""},
		{`kill -TERM "$$"`, -1, "", "signal/exception 15"},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		result, err := client.ExecGuest(ctx, guest.Node, vmid, []string{"/bin/sh", "-c", test.script})
		cancel()
		if test.failure == "" && (err != nil || result.ExitCode != test.exit || result.Stdout != test.output) {
			t.Fatal("normal Guest result changed", err, result.ExitCode)
		}
		if test.failure != "" && (err == nil || !strings.Contains(err.Error(), test.failure) || result.ExitCode != -1) {
			t.Fatal("abnormal Guest result looked successful", err, result.ExitCode)
		}
	}
	t.Logf("VM %d: normal stdout, nonzero exit and self-termination correctly distinguished; no Guest state changed", vmid)
}

func TestLivePCIResourceMappings(t *testing.T) {
	client := liveClient(t)
	mappings, err := client.PCIResourceMappings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("read %d PCI resource mappings", len(mappings))
}

func TestLiveMaintainedDesktopTemplates(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_TEMPLATE_AUDIT") != "true" {
		t.Skip("set VC_WORKSPACE_LIVE_TEMPLATE_AUDIT=true to audit the maintained VC Workspace templates")
	}
	client := liveClient(t)
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	expectations := []struct {
		vmid        int
		osType      string
		requireUEFI bool
		requireTPM  bool
	}{
		{vmid: 9100, osType: "l26"},
		{vmid: 9110, osType: "win10", requireUEFI: true, requireTPM: true},
		{vmid: 9111, osType: "win11", requireUEFI: true, requireTPM: true},
	}
	for _, expectation := range expectations {
		t.Run(fmt.Sprintf("vmid_%d", expectation.vmid), func(t *testing.T) {
			var template VM
			found := false
			for _, vm := range summary.VMs {
				if vm.VMID == expectation.vmid {
					template, found = vm, true
					break
				}
			}
			if !found {
				t.Fatalf("template VMID %d was not found", expectation.vmid)
			}
			if template.Kind != "qemu" || !template.Template || template.Status != "stopped" {
				t.Fatalf("VMID %d is not a stopped QEMU template: %#v", expectation.vmid, template)
			}
			configuration, err := client.VMConfiguration(t.Context(), template.Node, template.VMID)
			if err != nil {
				t.Fatal(err)
			}
			if configuration.OSType != expectation.osType || !configuration.AgentEnabled || configuration.Cores < 1 || configuration.MemoryMB < 512 {
				t.Fatalf("template VMID %d has an invalid base configuration: %#v", expectation.vmid, configuration)
			}
			if !containsString(configuration.Tags, "vc-workspace") && !containsString(configuration.Tags, "vc-vdi") {
				t.Fatalf("template VMID %d is missing the VC Workspace tag: %#v", expectation.vmid, configuration.Tags)
			}
			for _, requiredTag := range []string{"template", "rdp"} {
				if !containsString(configuration.Tags, requiredTag) {
					t.Fatalf("template VMID %d is missing tag %q: %#v", expectation.vmid, requiredTag, configuration.Tags)
				}
			}
			if len(configuration.Disks) == 0 || len(configuration.NetworkAdapters) == 0 {
				t.Fatalf("template VMID %d is missing disk or network hardware", expectation.vmid)
			}
			if len(configuration.PCIHostDevices) != 0 {
				t.Fatalf("base template VMID %d must not own a host PCI device", expectation.vmid)
			}
			if expectation.requireUEFI && (configuration.BIOS != "ovmf" || configuration.EFIDisk == "") {
				t.Fatalf("template VMID %d is missing OVMF/EFI state", expectation.vmid)
			}
			if expectation.requireTPM && (!strings.Contains(configuration.TPMState, "version=v2.0") || configuration.TPMState == "") {
				t.Fatalf("template VMID %d is missing TPM 2.0 state", expectation.vmid)
			}
			t.Logf("verified VMID %d (%s) on %s", template.VMID, configuration.OSType, template.Node)
		})
	}
}

func TestLiveGPUInventory(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_GPU_AUDIT") != "true" {
		t.Skip("set VC_WORKSPACE_LIVE_GPU_AUDIT=true to audit the live PVE GPU inventory")
	}
	client := liveClient(t)
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	nodes := make([]string, 0, len(summary.Nodes))
	for _, node := range summary.Nodes {
		if node.Status == "online" {
			nodes = append(nodes, node.Name)
		}
	}
	devices, err := client.GPUDevices(t.Context(), nodes)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) == 0 {
		t.Fatal("no display controller was found on an online PVE node")
	}
	for _, device := range devices {
		group := "none"
		if device.IOMMUGroup != nil {
			group = fmt.Sprint(*device.IOMMUGroup)
		}
		hardwareID := strings.TrimPrefix(device.VendorID, "0x") + ":" + strings.TrimPrefix(device.DeviceID, "0x")
		subsystemID := strings.TrimPrefix(device.SubsystemVendorID, "0x") + ":" + strings.TrimPrefix(device.SubsystemDeviceID, "0x")
		mdevTypes := make([]string, 0, len(device.MDevTypes))
		for _, kind := range device.MDevTypes {
			mdevTypes = append(mdevTypes, fmt.Sprintf("%s=%d", kind.Type, kind.Available))
		}
		if device.MDevCapable && len(mdevTypes) == 0 {
			t.Fatalf("%s %s reports mdev capability without any mediated-device types", device.Node, device.ID)
		}
		t.Logf("%s %s hardware=%s subsystem=%s iommu=%s assignable=%t mdev=%s", device.Node, device.ID, hardwareID, subsystemID, group, device.Assignable, strings.Join(mdevTypes, ","))
	}
}

func TestLiveGuestDesktopResolution(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_DESKTOP_RESOLUTION_AUDIT") != "true" {
		t.Skip("set VC_WORKSPACE_LIVE_DESKTOP_RESOLUTION_AUDIT=true to inspect a live guest desktop")
	}
	vmid, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_DESKTOP_VMID")))
	if err != nil || vmid <= 0 {
		t.Fatal("VC_WORKSPACE_LIVE_DESKTOP_VMID must be a positive VMID")
	}

	client := liveClient(t)
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var machine VM
	for _, candidate := range summary.VMs {
		if candidate.VMID == vmid {
			machine = candidate
			break
		}
	}
	if machine.VMID == 0 || machine.Kind != "qemu" || machine.Status != "running" {
		t.Fatalf("VMID %d is not a running QEMU guest", vmid)
	}

	sessionUser := strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_DESKTOP_USERNAME"))
	if sessionUser == "" {
		sessionUser = "vdi"
	}
	const script = `set -eu
session_user=$1
session_uid=$(id -u -- "$session_user")
session_pids=$(pgrep -u "$session_uid" -x xfce4-session)
test -n "$session_pids"
for session_pid in $session_pids; do
  display=$(tr '\0' '\n' < "/proc/$session_pid/environ" | sed -n 's/^DISPLAY=//p' | head -n1)
  xauth=$(tr '\0' '\n' < "/proc/$session_pid/environ" | sed -n 's/^XAUTHORITY=//p' | head -n1)
  test -n "$display"
  geometry=$(runuser -u "$session_user" -- env DISPLAY="$display" XAUTHORITY="$xauth" xrandr --current | awk '$0 ~ / connected / { print; exit }')
  resolution=$(printf '%s\n' "$geometry" | awk '{ for (i=1; i<=NF; i++) if ($i ~ /^[0-9]+x[0-9]+\+/) { split($i, value, "+"); print value[1]; exit } }')
  dpi=$(runuser -u "$session_user" -- env DISPLAY="$display" XAUTHORITY="$xauth" xdpyinfo | awk '/resolution:/ { print $2; exit }')
  xfce_dpi=$(runuser -u "$session_user" -- env DISPLAY="$display" XAUTHORITY="$xauth" xfconf-query -c xsettings -p /Xft/DPI 2>/dev/null || true)
  printf '%s:%s=%s geometry=%s dpi=%s xfce_dpi=%s\n' "$session_pid" "$display" "$resolution" "$geometry" "$dpi" "$xfce_dpi"
done`
	result, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, []string{"/bin/sh", "-lc", script, "vc-workspace-resolution-probe", sessionUser})
	if err != nil {
		t.Fatal(err)
	}
	sessions := strings.TrimSpace(result.Stdout)
	if result.ExitCode != 0 || sessions == "" {
		t.Fatalf("guest resolution probe failed: exit=%d stderr=%q", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	if expected := strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_DESKTOP_EXPECTED_RESOLUTION")); expected != "" &&
		!strings.Contains(sessions, "="+expected) {
		t.Fatalf("guest sessions are %q; no desktop has expected resolution %s", sessions, expected)
	}
	t.Logf("VMID %d desktop sessions:\n%s", vmid, sessions)
}

func TestLiveGuestDesktopDPI(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_DESKTOP_DPI_APPLY") != "true" {
		t.Skip("set VC_WORKSPACE_LIVE_DESKTOP_DPI_APPLY=true to apply XFCE DPI to a live desktop")
	}
	vmid, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_DESKTOP_VMID")))
	if err != nil || vmid <= 0 {
		t.Fatal("VC_WORKSPACE_LIVE_DESKTOP_VMID must be a positive VMID")
	}
	dpi, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_DESKTOP_DPI")))
	if err != nil || (dpi != 96 && dpi != 192) {
		t.Fatal("VC_WORKSPACE_LIVE_DESKTOP_DPI must be 96 or 192")
	}

	client := liveClient(t)
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var machine VM
	for _, candidate := range summary.VMs {
		if candidate.VMID == vmid {
			machine = candidate
			break
		}
	}
	if machine.VMID == 0 || machine.Kind != "qemu" || machine.Status != "running" {
		t.Fatalf("VMID %d is not a running QEMU guest", vmid)
	}

	script := fmt.Sprintf(`set -eu
session_pids=$(pgrep -u vdi -x xfce4-session)
test -n "$session_pids"
for session_pid in $session_pids; do
  display=$(tr '\0' '\n' < "/proc/$session_pid/environ" | sed -n 's/^DISPLAY=//p' | head -n1)
  xauth=$(tr '\0' '\n' < "/proc/$session_pid/environ" | sed -n 's/^XAUTHORITY=//p' | head -n1)
  test -n "$display"
  runuser -u vdi -- env DISPLAY="$display" XAUTHORITY="$xauth" xfconf-query -c xsettings -p /Xft/DPI -s %d
  actual=$(runuser -u vdi -- env DISPLAY="$display" XAUTHORITY="$xauth" xfconf-query -c xsettings -p /Xft/DPI)
  printf '%%s:%%s dpi=%%s\n' "$session_pid" "$display" "$actual"
done`, dpi)
	result, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, []string{"/bin/sh", "-lc", script})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, fmt.Sprintf("dpi=%d", dpi)) {
		t.Fatalf("guest DPI apply failed: exit=%d stdout=%q stderr=%q", result.ExitCode, strings.TrimSpace(result.Stdout), strings.TrimSpace(result.Stderr))
	}
	t.Logf("VMID %d desktop DPI:\n%s", vmid, strings.TrimSpace(result.Stdout))
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func liveClient(t *testing.T) *Client {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_PVE_ENDPOINT"))
	credentialFile := strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE"))
	if endpoint == "" || credentialFile == "" {
		t.Skip("set VC_WORKSPACE_LIVE_PVE_ENDPOINT and VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE to run read-only PVE checks")
	}
	contents, err := os.ReadFile(credentialFile)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(contents))
	if len(fields) != 3 || fields[1] != "/" {
		t.Fatal("PVE credential file must contain username@realm / password")
	}
	client, err := New(Config{
		Endpoint: endpoint, Username: fields[0], Password: fields[2],
		MutationsEnabled: os.Getenv("VC_WORKSPACE_LIVE_PVE_MUTATIONS_ENABLED") == "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
