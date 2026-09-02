package httpapi

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/virtual-cable/vc-vdi/internal/pve"
)

// This destructive opt-in check toggles the real desktop account through the
// exact policy commands used by the server, verifies both states, and restores
// the original state. It may sign out an active vdi session during downgrade.
func TestLiveLinuxDesktopPrivilegePolicy(t *testing.T) {
	if os.Getenv("VC_VDI_LIVE_PRIVILEGE_AUDIT") != "true" {
		t.Skip("set VC_VDI_LIVE_PRIVILEGE_AUDIT=true and VC_VDI_LIVE_PRIVILEGE_VMID to test a real Linux desktop")
	}
	vmid, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VC_VDI_LIVE_PRIVILEGE_VMID")))
	if err != nil || vmid <= 0 {
		t.Fatal("VC_VDI_LIVE_PRIVILEGE_VMID must be a positive VMID")
	}
	client := livePrivilegeClient(t)
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var machine pve.VM
	for _, candidate := range summary.VMs {
		if candidate.VMID == vmid {
			machine = candidate
			break
		}
	}
	if machine.VMID == 0 || machine.Kind != "qemu" || machine.Status != "running" || !isManagedDesktop(machine) {
		t.Fatalf("VMID %d is not a running managed QEMU desktop", vmid)
	}
	configuration, err := client.VMConfiguration(t.Context(), machine.Node, machine.VMID)
	if err != nil {
		t.Fatal(err)
	}
	if desktopOSFamily(configuration.OSType) != "linux" {
		t.Fatalf("VMID %d is not a Linux desktop", vmid)
	}
	groups, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, []string{"/bin/sh", "-c", "id -nG vdi"})
	if err != nil || groups.ExitCode != 0 {
		t.Fatalf("read original vdi groups: result=%#v error=%v", groups, err)
	}
	originallyAdmin := containsWord(groups.Stdout, "sudo")
	defer func() {
		mode := "standard"
		if originallyAdmin {
			mode = "local_admin"
		}
		command, commandErr := desktopAccessPolicyCommand("linux", mode)
		if commandErr != nil {
			t.Errorf("build restore command: %v", commandErr)
			return
		}
		result, restoreErr := client.ExecGuest(t.Context(), machine.Node, machine.VMID, command)
		if restoreErr != nil || result.ExitCode != 0 {
			t.Errorf("restore original privilege mode: result=%#v error=%v", result, restoreErr)
		}
	}()

	for _, test := range []struct {
		mode      string
		wantAdmin bool
	}{{mode: "local_admin", wantAdmin: true}, {mode: "standard", wantAdmin: false}} {
		command, err := desktopAccessPolicyCommand("linux", test.mode)
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, command)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("apply %s: result=%#v error=%v", test.mode, result, err)
		}
		verified, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, []string{"/bin/sh", "-c", "id -nG vdi"})
		if err != nil || verified.ExitCode != 0 || containsWord(verified.Stdout, "sudo") != test.wantAdmin {
			t.Fatalf("verify %s: result=%#v error=%v", test.mode, verified, err)
		}
		sudoCheck, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, []string{"/bin/sh", "-c", "runuser -u vdi -- sudo -n /usr/bin/true"})
		if err != nil || (sudoCheck.ExitCode == 0) != test.wantAdmin {
			t.Fatalf("verify non-interactive sudo for %s: result=%#v error=%v", test.mode, sudoCheck, err)
		}
	}
}

func TestLiveWindowsDesktopPrivilegePolicy(t *testing.T) {
	if os.Getenv("VC_VDI_LIVE_WINDOWS_PRIVILEGE_AUDIT") != "true" {
		t.Skip("set VC_VDI_LIVE_WINDOWS_PRIVILEGE_AUDIT=true and VC_VDI_LIVE_PRIVILEGE_VMID to test a real Windows desktop")
	}
	vmid, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VC_VDI_LIVE_PRIVILEGE_VMID")))
	if err != nil || vmid <= 0 {
		t.Fatal("VC_VDI_LIVE_PRIVILEGE_VMID must be a positive VMID")
	}
	client := livePrivilegeClient(t)
	machine := liveMachine(t, client, vmid)
	if machine.Kind != "qemu" || machine.Template || !isManagedDesktop(machine) {
		t.Fatalf("VMID %d is not a managed QEMU desktop", vmid)
	}
	configuration, err := client.VMConfiguration(t.Context(), machine.Node, machine.VMID)
	if err != nil {
		t.Fatal(err)
	}
	if desktopOSFamily(configuration.OSType) != "windows" {
		t.Fatalf("VMID %d is not a Windows desktop", vmid)
	}
	wasStopped := machine.Status == "stopped"
	if wasStopped {
		upid, err := client.ChangePowerState(t.Context(), machine.Node, machine.VMID, "start")
		if err != nil {
			t.Fatal(err)
		}
		waitLiveTask(t, client, machine.Node, upid)
	}
	defer func() {
		if wasStopped {
			shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			upid, stopErr := client.ChangePowerState(shutdownContext, machine.Node, machine.VMID, "shutdown")
			if stopErr != nil {
				t.Errorf("restore stopped state: %v", stopErr)
				return
			}
			waitLiveTask(t, client, machine.Node, upid)
		}
	}()

	originallyAdmin := waitWindowsPrivilegeState(t, client, machine, 5*time.Minute)
	defer func() {
		mode := "standard"
		if originallyAdmin {
			mode = "local_admin"
		}
		command, commandErr := desktopAccessPolicyCommand("windows", mode)
		if commandErr != nil {
			t.Errorf("build restore command: %v", commandErr)
			return
		}
		restoreContext, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		result, restoreErr := client.ExecGuest(restoreContext, machine.Node, machine.VMID, command)
		if restoreErr != nil || result.ExitCode != 0 {
			t.Errorf("restore original privilege mode: result=%#v error=%v", result, restoreErr)
		}
	}()

	for _, test := range []struct {
		mode      string
		wantAdmin bool
	}{{mode: "local_admin", wantAdmin: true}, {mode: "standard", wantAdmin: false}} {
		command, err := desktopAccessPolicyCommand("windows", test.mode)
		if err != nil {
			t.Fatal(err)
		}
		result, err := liveExecGuest(t, client, machine, command)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("apply %s: result=%#v error=%v", test.mode, result, err)
		}
		if actual := windowsPrivilegeState(t, client, machine); actual != test.wantAdmin {
			t.Fatalf("verify %s: administrator=%t, want %t", test.mode, actual, test.wantAdmin)
		}
	}
}

func liveMachine(t *testing.T, client *pve.Client, vmid int) pve.VM {
	t.Helper()
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range summary.VMs {
		if candidate.VMID == vmid {
			return candidate
		}
	}
	t.Fatalf("VMID %d was not found", vmid)
	return pve.VM{}
}

func waitLiveTask(t *testing.T, client *pve.Client, node, upid string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		status, err := client.TaskStatus(t.Context(), node, upid)
		if err != nil {
			t.Fatal(err)
		}
		if status.Status == "stopped" {
			if status.ExitStatus != "OK" {
				t.Fatalf("PVE task failed: %s", status.ExitStatus)
			}
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("PVE task did not finish before timeout")
}

func waitWindowsPrivilegeState(t *testing.T, client *pve.Client, machine pve.VM, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastResult pve.GuestExecResult
	var lastErr error
	for time.Now().Before(deadline) {
		commandContext, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		result, err := windowsPrivilegeStateResult(commandContext, client, machine)
		cancel()
		if err == nil {
			if result.ExitCode != 0 {
				t.Fatalf("Windows Guest Agent command failed: result=%#v", result)
			}
			return strings.Contains(result.Stdout, "local_admin")
		}
		lastResult, lastErr = result, err
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("Windows QEMU Guest Agent did not become ready before timeout: result=%#v error=%v", lastResult, lastErr)
	return false
}

func windowsPrivilegeState(t *testing.T, client *pve.Client, machine pve.VM) bool {
	t.Helper()
	commandContext, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	result, err := windowsPrivilegeStateResult(commandContext, client, machine)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("read Windows privilege state: result=%#v error=%v", result, err)
	}
	return strings.Contains(result.Stdout, "local_admin")
}

func windowsPrivilegeStateResult(ctx context.Context, client *pve.Client, machine pve.VM) (pve.GuestExecResult, error) {
	script := `$ErrorActionPreference='Stop'; $user=Get-LocalUser -Name 'vdi'; $administrators=(Get-LocalGroup -SID 'S-1-5-32-544').Name; if (Get-LocalGroupMember -Group $administrators | Where-Object { $_.SID -eq $user.SID }) { Write-Output 'local_admin' } else { Write-Output 'standard' }`
	return client.ExecGuest(ctx, machine.Node, machine.VMID, []string{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script})
}

func liveExecGuest(t *testing.T, client *pve.Client, machine pve.VM, command []string) (pve.GuestExecResult, error) {
	t.Helper()
	commandContext, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	return client.ExecGuest(commandContext, machine.Node, machine.VMID, command)
}

func livePrivilegeClient(t *testing.T) *pve.Client {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("VC_VDI_LIVE_PVE_ENDPOINT"))
	credentialFile := strings.TrimSpace(os.Getenv("VC_VDI_LIVE_PVE_CREDENTIAL_FILE"))
	if endpoint == "" || credentialFile == "" {
		t.Fatal("VC_VDI_LIVE_PVE_ENDPOINT and VC_VDI_LIVE_PVE_CREDENTIAL_FILE are required")
	}
	contents, err := os.ReadFile(credentialFile)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(contents))
	if len(fields) != 3 || fields[1] != "/" {
		t.Fatal("PVE credential file must contain username@realm / password")
	}
	client, err := pve.New(pve.Config{Endpoint: endpoint, Username: fields[0], Password: fields[2], MutationsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func containsWord(value, expected string) bool {
	for _, word := range strings.Fields(value) {
		if word == expected {
			return true
		}
	}
	return false
}
