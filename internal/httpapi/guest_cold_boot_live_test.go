package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	linuxguest "github.com/Veritas-Calculus/vc-workspace/deploy/guest/linux"
	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

// Opt-in power cycle of one idle acceptance VM, never a business desktop. The
// restoration manifest is on persistent storage and precedes boot enablement.
func TestLiveLinuxGuestColdBootExpiry(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_GUEST_COLD_BOOT_AUDIT") != "true" {
		t.Skip("explicit isolated Guest power-cycle acceptance required")
	}
	vmid, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID"))
	if err != nil || vmid <= 0 || vmid == 158 {
		t.Fatal("explicit non-business VMID required")
	}
	client := livePrivilegeClient(t)
	machine := liveMachine(t, client, vmid)
	if machine.Status != "running" || machine.Template || !isManagedDesktop(machine) || !strings.HasSuffix(machine.Name, "-check") {
		t.Fatal("running isolated acceptance desktop required")
	}
	configuration, err := client.VMConfiguration(t.Context(), machine.Node, vmid)
	if err != nil || configuration.OSType != "l26" {
		t.Fatal("Linux required", err)
	}
	exec := func(script string) string {
		t.Helper()
		result, err := client.ExecGuest(t.Context(), machine.Node, vmid, []string{"/bin/sh", "-c", script})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("cold boot assertion: exit=%d stderr=%s transport=%v", result.ExitCode, result.Stderr, err)
		}
		return strings.TrimSpace(result.Stdout)
	}
	exec("set -eu\ntest \"$(. /etc/os-release; echo $VERSION_ID)\" = 13\ntest -z \"$(ps -C Xorg -C xrdp-sesexec -o pid=)\"\ntest -z \"$(getent -s files passwd | awk -F: '$1 ~ /^vca/ {print $1}')\"\ntest -z \"$(loginctl list-sessions --no-legend)\"")
	boot := exec("cat /proc/sys/kernel/random/boot_id")
	if len(boot) != 36 {
		t.Fatal("invalid original kernel boot ID")
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	marker := "svc" + hex.EncodeToString(nonce[:])
	name := store.AgentGuestUsername(marker)
	t.Logf("durable cold-boot fixture /var/lib/vc-workspace/acceptance-%s on VM %d", marker, vmid)
	units := map[string]string{}
	for _, unit := range []string{"vc-workspace-agent.service", "vc-workspace-accounts.service", "vc-workspace-accounts.timer"} {
		raw, err := linuxguest.Units.ReadFile(unit)
		if err != nil {
			t.Fatal(err)
		}
		units[unit] = string(raw)
	}
	fixture := func(ctx context.Context, operation string, lease computer.AccountLease) error {
		request := map[string]any{"units": units, "login_fence_installer": linuxguest.LoginFenceInstaller}
		if operation == "bind" || operation == "bind-next" {
			request = map[string]any{"account": lease}
		}
		input, _ := json.Marshal(request)
		result, err := client.ExecGuestWithInput(ctx, machine.Node, vmid, []string{"/usr/bin/python3", "-c", guestServiceFixture, operation, marker, "cold-boot"}, input)
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("cold fixture %s: exit=%d stderr=%s transport=%v", operation, result.ExitCode, result.Stderr, err)
		}
		return nil
	}
	executor := computer.NewPVEExecutor(client)
	var lease computer.AccountLease
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()
		if err := fixture(ctx, "stop", lease); err != nil {
			t.Error("retaining durable recovery manifest", err)
			return
		}
		if lease.Identity.UID != 0 {
			if err := executor.StopAgentSession(ctx, machine, lease); err != nil {
				t.Error("retaining unresolved exact account", err)
				return
			}
			command := fmt.Sprintf("set -eu\ntest \"$(getent -s files passwd %s | cut -d: -f3)\" = %d\nunit=user@%d.service\ncase \"$(systemctl show \"$unit\" --property=Job --value)\" in ''|0) ;; *) exit 1;; esac\ncase \"$(systemctl show \"$unit\" --property=ActiveState --value)\" in inactive) ;; failed) systemctl reset-failed \"$unit\";; *) exit 1;; esac\nuserdel -r %s\nrm -rf -- /var/lib/vc-workspace/computer-v2/users/%s\n! getent -s files passwd %s", name, lease.Identity.UID, lease.Identity.UID, name, name, name)
			result, err := client.ExecGuest(ctx, machine.Node, vmid, []string{"/bin/sh", "-c", command})
			if err != nil || result.ExitCode != 0 {
				t.Error("cold boot account cleanup failed", err, result.ExitCode)
				return
			}
		}
		if err := fixture(ctx, "restore", lease); err != nil {
			t.Error(err)
		}
	})
	if err := fixture(t.Context(), "setup", lease); err != nil {
		t.Fatal(err)
	}
	exec("! getent passwd " + name + " && test ! -e /home/" + name + " && test ! -e /var/lib/vc-workspace/computer-v2/users/" + name)
	identity, err := executor.PrepareAgentAccount(t.Context(), machine, name)
	if err != nil {
		t.Fatal(err)
	}
	lease = computer.AccountLease{SchemaVersion: 1, Identity: identity, LeaseID: "lease_" + marker, ControlEpoch: 1, LoginGeneration: 1, ExpiresUnixSeconds: time.Now().Add(90 * time.Second).Unix()}
	if err := fixture(t.Context(), "bind", lease); err != nil {
		t.Fatal(err)
	}
	target, err := executor.StartAgentSession(t.Context(), machine, lease)
	if err != nil || target.UID != identity.UID {
		t.Fatal("original desktop login failed", err)
	}
	before, err := executor.InspectAgentAccount(t.Context(), machine, name)
	if err != nil || !before.Matches(lease, "sealed") || before.LoginWritersAbsent == nil || *before.LoginWritersAbsent {
		t.Fatal("cold-boot fixture did not seal a real login domain", err)
	}
	markerPath := "/home/" + name + "/acceptance-cold-boot"
	exec("runuser -u " + name + " -- /bin/sh -c 'printf %s " + marker + " > " + markerPath + "'")
	// No other logged-in user may have arrived since initial admission.
	exec(fmt.Sprintf("loginctl list-sessions --no-legend | awk '$2 != %d {exit 1}'", identity.UID))
	refresh := func() (pve.Summary, pve.VM, error) {
		summary, err := client.Summary(t.Context())
		for _, candidate := range summary.VMs {
			if candidate.VMID == vmid {
				if candidate.Node != machine.Node || candidate.Name != machine.Name || candidate.Template {
					return summary, candidate, fmt.Errorf("acceptance VM changed")
				}
				return summary, candidate, err
			}
		}
		return summary, pve.VM{}, fmt.Errorf("acceptance VM unavailable: %v", err)
	}
	power := func(action, expected string) {
		t.Helper()
		summary, current, err := refresh()
		if err == nil {
			current.Status, err = client.VMPowerState(t.Context(), machine.Node, vmid)
		}
		if err != nil || current.Status != expected {
			t.Fatal("power precondition changed", current.Status, err)
		}
		if action == "start" {
			if current.MemoryTotal <= 0 || current.MemoryTotal != int64(configuration.MemoryMB)<<20 {
				t.Fatal("acceptance VM memory changed while powered off")
			}
			available := false
			for _, node := range summary.Nodes {
				if node.Name == machine.Node && node.Status == "online" {
					reserve := max(int64(2<<30), node.MemoryTotal/10)
					available = node.MemoryTotal-node.MemoryUsed >= current.MemoryTotal+reserve
				}
			}
			if !available {
				t.Fatal("insufficient host memory for cold-boot restart; durable fixture retained")
			}
		}
		upid, err := client.ChangePowerState(t.Context(), machine.Node, vmid, action)
		if err != nil {
			t.Fatal("power dispatch uncertain; inspect authoritative VM/task state before recovery", err)
		}
		t.Logf("single %s request: %s", action, upid)
		deadline := time.Now().Add(2 * time.Minute)
		for {
			status, err := client.TaskStatus(t.Context(), machine.Node, upid)
			if err == nil && status.Status == "stopped" {
				if status.ExitStatus != "OK" {
					t.Fatal("power task failed", status.ExitStatus)
				}
				wanted := "stopped"
				if action == "start" {
					wanted = "running"
				}
				actual, observeErr := client.VMPowerState(t.Context(), machine.Node, vmid)
				if observeErr == nil && actual == wanted {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("same power task remains unresolved; no redispatch", upid, err)
			}
			time.Sleep(time.Second)
		}
	}
	power("shutdown", "running")
	stopped, err := client.VMPowerState(t.Context(), machine.Node, vmid)
	if err != nil || stopped != "stopped" {
		t.Fatal("power-off not confirmed", err)
	}
	t.Log("confirmed power-off; waiting for the original lease deadline without changing clocks or journals")
	for time.Now().Unix() <= lease.ExpiresUnixSeconds {
		select {
		case <-t.Context().Done():
			t.Fatal("offline deadline wait canceled; persistent recovery manifest retained")
		case <-time.After(time.Second):
		}
	}
	power("start", "stopped")
	deadline := time.Now().Add(2 * time.Minute)
	for {
		result, err := client.ExecGuest(t.Context(), machine.Node, vmid, []string{"/bin/cat", "/proc/sys/kernel/random/boot_id"})
		if err == nil && result.ExitCode == 0 && len(strings.TrimSpace(result.Stdout)) == 36 && strings.TrimSpace(result.Stdout) != boot {
			t.Log("QGA returned on a new kernel boot; no manual Guest service start/reconciliation")
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cold boot QGA did not recover; do not repeat start", err)
		}
		time.Sleep(time.Second)
	}
	deadline = time.Now().Add(45 * time.Second)
	for {
		value, err := executor.InspectAgentAccount(t.Context(), machine, name)
		closed := lease
		closed.ExpiresUnixSeconds = 0
		if err == nil && value.Matches(closed, "revoked") && value.LoginStopped() {
			break
		}
		if time.Now().After(deadline) {
			t.Log(exec("systemctl show vc-workspace-accounts.service --property=ActiveState,Result,ExecMainStatus\njournalctl -b -u vc-workspace-accounts.service --no-pager -n 15"))
			t.Fatal("cold-boot timer did not close the expired exact login", err)
		}
		time.Sleep(time.Second)
	}
	exec("set -eu\nsystemctl is-enabled vc-workspace-agent.service vc-workspace-accounts.timer\nsystemctl is-active vc-workspace-agent.service vc-workspace-accounts.timer\npython3 -c 'import json,time; from pathlib import Path; p=Path(\"/var/lib/vc-workspace\"); assert time.time()-json.loads((p/\"agent-state.json\").read_text())[\"heartbeat_unix\"]<25; assert (p/\"desktop-ready\").is_file()'")
	if exec("cat "+markerPath) != marker {
		t.Fatal("power cycle lost the fixed account Home")
	}
	t.Log("boot-enabled canonical timer revoked the original expired lease; UID/Home persisted and old-boot login writers are absent")
	next := lease
	next.LeaseID += "_next"
	next.ControlEpoch++
	next.LoginGeneration++
	next.ExpiresUnixSeconds = time.Now().Add(55 * time.Second).Unix()
	// Persist each exact intent before dispatch, so losing this test process
	// during post-boot login does not leave only the old epoch in recovery.
	if err := fixture(t.Context(), "bind-next", next); err != nil {
		t.Fatal("retaining original lease recovery record", err)
	}
	lease = next
	after, err := executor.StartAgentSession(t.Context(), machine, lease)
	if err != nil || after.UID != target.UID || after.InstanceID == target.InstanceID || exec("cat "+markerPath) != marker {
		t.Fatal("fresh desktop did not recover the same identity/Home after cold boot", err)
	}
	t.Log("fresh post-boot lease established a new real Helper on the original UID/Home")
}
