package httpapi

import (
	"context"
	"crypto/rand"
	_ "embed"
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

//go:embed guest_service_fixture.py
var guestServiceFixture string

//go:embed guest_login_birth_fixture.py
var guestLoginBirthFixture string

// Only after the owned test account has been revoked and periodic writers
// stopped. Diagnostic failure metadata must not contaminate later UID reuse;
// never reset a running unit, a pending job, or an unrelated account's state.
func clearLiveLinuxAccountFailures(ctx context.Context, client *pve.Client, node string, vmid int, identity computer.AccountIdentity) error {
	if identity.UID < 1000 || identity.SID != "" || len(identity.Username) != 15 || !strings.HasPrefix(identity.Username, "vca") {
		return fmt.Errorf("invalid isolated account identity")
	}
	if _, err := hex.DecodeString(identity.Username[3:]); err != nil {
		return fmt.Errorf("invalid isolated account username")
	}
	script := fmt.Sprintf(`set -eu
test "$(getent -s files passwd %s | cut -d: -f3)" = %d
status=0
pgrep -u %d >/dev/null || status=$?
test "$status" = 1
failed_units=
for unit in user@%d.service user-%d.slice user-runtime-dir@%d.service; do
    case "$(systemctl show "$unit" --property=Job --value)" in ''|0) ;; *) exit 1;; esac
    case "$(systemctl show "$unit" --property=ActiveState --value)" in
        inactive) ;;
        failed) failed_units="$failed_units $unit" ;;
        *) exit 1;;
    esac
done
for unit in $failed_units; do systemctl reset-failed "$unit"; done
`, identity.Username, identity.UID, identity.UID, identity.UID, identity.UID, identity.UID)
	result, err := client.ExecGuest(ctx, node, vmid, []string{"/bin/sh", "-c", script})
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("owned account failure metadata: exit=%d transport=%v", result.ExitCode, err)
	}
	return nil
}

// This uses the exact shipped systemd units on an otherwise idle acceptance VM.
// No host drivers, existing services, business desktop or database are changed.
func TestLiveLinuxGuestServices(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_GUEST_SERVICE_AUDIT") != "true" {
		t.Skip("explicit isolated Linux service acceptance required")
	}
	vmid, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID"))
	if err != nil || vmid <= 0 || vmid == 158 {
		t.Fatal("explicit non-business acceptance VMID required")
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
			t.Fatalf("service fixture command failed: exit=%d stderr=%s transport=%v", result.ExitCode, result.Stderr, err)
		}
		return strings.TrimSpace(result.Stdout)
	}
	exec("set -eu\ntest \"$(. /etc/os-release; echo $VERSION_ID)\" = 13\ntest -z \"$(ps -C Xorg -o pid=)\"\ntest -z \"$(getent -s files passwd | awk -F: '$1 ~ /^vca/ {print $1}')\"\n/usr/local/sbin/vc-workspace-guest-agent --help | grep -q -- --readiness-only")
	xrdpBefore := exec("sha256sum /etc/xrdp/xrdp.ini /etc/xrdp/sesman.ini /etc/pam.d/xrdp-sesman")
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	marker := "svc" + hex.EncodeToString(nonce[:])
	t.Logf("isolated service fixture %s on VM %d", marker, vmid)
	units := map[string]string{}
	for _, name := range []string{"vc-workspace-agent.service", "vc-workspace-accounts.service", "vc-workspace-accounts.timer"} {
		raw, err := linuxguest.Units.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		units[name] = string(raw)
	}
	fixture := func(ctx context.Context, operation string) error {
		payload, _ := pve.MarshalGuestJSON(map[string]any{"units": units, "login_fence_installer": linuxguest.LoginFenceInstaller})
		result, err := client.ExecGuestWithInput(ctx, machine.Node, vmid, []string{"/usr/bin/python3", "-c", guestServiceFixture, operation, marker}, payload)
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("service fixture %s: exit=%d stderr=%s transport=%v", operation, result.ExitCode, result.Stderr, err)
		}
		return nil
	}
	executor := computer.NewPVEExecutor(client)
	var owned []computer.AccountLease
	var birthUser, birthCheckpoint string
	birthFixture := func(ctx context.Context, operation string) (string, error) {
		result, err := client.ExecGuestWithInput(ctx, machine.Node, vmid, []string{"/usr/bin/python3", "-c", guestLoginBirthFixture, operation, marker, birthUser, birthCheckpoint}, []byte(guestLoginBirthFixture))
		if err != nil || result.ExitCode != 0 {
			return "", fmt.Errorf("birth fixture %s: exit=%d stderr=%s transport=%v", operation, result.ExitCode, result.Stderr, err)
		}
		var value struct {
			Stage string `json:"stage"`
		}
		if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil || value.Stage == "" {
			return "", fmt.Errorf("birth fixture %s: incomplete receipt", operation)
		}
		return value.Stage, nil
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		if err := fixture(ctx, "stop"); err != nil {
			t.Error(err)
			return
		}
		if birthUser != "" {
			if _, err := birthFixture(ctx, "restore"); err != nil {
				t.Error("retaining exact paused-login recovery fixture", err)
				return
			}
		}
		clean := true
		for _, intent := range owned {
			if intent.Identity.UID == 0 {
				observed, err := executor.InspectAgentAccount(ctx, machine, intent.Identity.Username)
				if err != nil {
					t.Error("uncertain fixture provisioning", err)
					clean = false
					continue
				}
				if observed.Identity == nil {
					// Inspection can itself leave a root-owned lock directory.
					// Keep the recovery manifest rather than claim all resources
					// were removed without a proven account binding.
					t.Error("retaining unbound fixture provisioning for", intent.Identity.Username)
					clean = false
					continue
				}
				intent.Identity = *observed.Identity
			}
			if err := executor.StopAgentSession(ctx, machine, intent); err != nil {
				t.Error("retaining unresolved exact fixture identity", err)
				clean = false
				continue
			}
			if err := clearLiveLinuxAccountFailures(ctx, client, machine.Node, vmid, intent.Identity); err != nil {
				t.Error(err)
				clean = false
				continue
			}
			script := fmt.Sprintf("set -eu\ntest \"$(getent -s files passwd %s | cut -d: -f3)\" = %d\nuserdel -r %s\nrm -rf -- /var/lib/vc-workspace/computer-v2/users/%s\n! getent -s files passwd %s", intent.Identity.Username, intent.Identity.UID, intent.Identity.Username, intent.Identity.Username, intent.Identity.Username)
			result, err := client.ExecGuest(ctx, machine.Node, vmid, []string{"/bin/sh", "-c", script})
			if err != nil || result.ExitCode != 0 {
				t.Error("fixture account cleanup failed", err, result.ExitCode)
				clean = false
			}
		}
		if !clean {
			return
		}
		if err := fixture(ctx, "restore"); err != nil {
			t.Error(err)
		}
		result, err := client.ExecGuest(ctx, machine.Node, vmid, []string{"sha256sum", "/etc/xrdp/xrdp.ini", "/etc/xrdp/sesman.ini", "/etc/pam.d/xrdp-sesman"})
		if err != nil || strings.TrimSpace(result.Stdout) != xrdpBefore {
			t.Error("xrdp configuration changed", err)
		}
	})
	if err := fixture(t.Context(), "setup"); err != nil {
		t.Fatal(err)
	}
	property := func(unit, key string) string {
		return exec("systemctl show " + unit + " --property=" + key + " --value")
	}
	if property("vc-workspace-accounts.timer", "ActiveState") != "active" {
		t.Fatal("production timer not active")
	}
	if property("vc-workspace-accounts.service", "ReadWritePaths") != "/var/lib/vc-workspace/computer-v2 /etc /sys/fs/cgroup/user.slice /tmp" || property("vc-workspace-accounts.service", "PrivateTmp") != "no" {
		t.Fatal("worker permission layout differs from source")
	}
	if property("vc-workspace-agent.service", "ReadWritePaths") != "/var/lib/vc-workspace" {
		t.Fatal("readiness service can modify account databases")
	}
	allocate := func(index int) computer.AccountLease {
		t.Helper()
		name := store.AgentGuestUsername(fmt.Sprintf("%s-%d", marker, index))
		exec("! getent passwd " + name + " && test ! -e /home/" + name + " && test ! -e /var/lib/vc-workspace/computer-v2/users/" + name)
		intent := computer.AccountLease{SchemaVersion: 1, Identity: computer.AccountIdentity{Username: name}, LeaseID: "lease_" + marker, ControlEpoch: 1, LoginGeneration: 1}
		owned = append(owned, intent)
		identity, err := executor.PrepareAgentAccount(t.Context(), machine, name)
		if err != nil {
			t.Fatal(err)
		}
		intent.Identity, intent.ExpiresUnixSeconds = identity, time.Now().Add(55*time.Second).Unix()
		owned[len(owned)-1] = intent
		return intent
	}
	prepare := func(index int) computer.AccountLease {
		t.Helper()
		intent := allocate(index)
		target, err := executor.StartAgentSession(t.Context(), machine, intent)
		if err != nil || target.UID != intent.Identity.UID {
			t.Fatal("real service acceptance desktop did not start", err)
		}
		value, err := executor.InspectAgentAccount(t.Context(), machine, intent.Identity.Username)
		if err != nil || !value.Matches(intent, "sealed") || value.ProcessesAbsent == nil || *value.ProcessesAbsent || value.LoginWritersAbsent == nil || *value.LoginWritersAbsent {
			t.Fatal("live desktop must retain its exact registered root creator", err)
		}
		return intent
	}
	wait := func(timeout time.Duration, condition func() bool) {
		t.Helper()
		until := time.Now().Add(timeout)
		for !condition() {
			if !time.Now().Before(until) {
				t.Fatal("service condition did not converge before its bounded deadline")
			}
			select {
			case <-t.Context().Done():
				t.Fatal("test canceled")
			case <-time.After(time.Second):
			}
		}
	}
	closed := func(intent computer.AccountLease) bool {
		value, err := executor.InspectAgentAccount(t.Context(), machine, intent.Identity.Username)
		intent.ExpiresUnixSeconds = 0
		return err == nil && value.Matches(intent, "revoked") && value.LoginStopped()
	}
	first := prepare(0)
	oldPID := property("vc-workspace-agent.service", "MainPID")
	if oldPID == "0" {
		t.Fatal("readiness process absent")
	}
	exec("systemctl kill --kill-whom=main --signal=SIGKILL vc-workspace-agent.service")
	wait(15*time.Second, func() bool {
		current := property("vc-workspace-agent.service", "MainPID")
		return current != "0" && current != oldPID
	})
	if time.Now().Unix() >= first.ExpiresUnixSeconds || closed(first) {
		t.Fatal("fixture did not observe a live lease after service restart")
	}
	wait(time.Until(time.Unix(first.ExpiresUnixSeconds, 0))+40*time.Second, func() bool { return closed(first) })
	t.Log("exact canonical services: readiness SIGKILL restarted; original deadline locked shadow and removed real xrdp/Helper processes and registered root creators")
	second := prepare(1)
	if err := fixture(t.Context(), "readonly"); err != nil {
		t.Fatal(err)
	}
	wait(time.Until(time.Unix(second.ExpiresUnixSeconds, 0))+40*time.Second, func() bool {
		value, err := executor.InspectAgentAccount(t.Context(), machine, second.Identity.Username)
		return err == nil && value.Lifecycle != nil && value.Lifecycle.Phase == "revoked" && !value.Disabled && value.ProcessesAbsent != nil && !*value.ProcessesAbsent && property("vc-workspace-accounts.service", "Result") == "exit-code"
	})
	if err := fixture(t.Context(), "repair"); err != nil {
		t.Fatal(err)
	}
	// Do not manually start/reconcile here: the production timer must recover.
	wait(40*time.Second, func() bool { return closed(second) })
	if property("vc-workspace-accounts.timer", "ActiveState") != "active" {
		t.Fatal("timer stopped after worker failure")
	}
	// Independent account observation may finish just before systemd collects
	// the worker's exit status. Wait for the same bounded run, not a new sweep.
	wait(5*time.Second, func() bool {
		return property("vc-workspace-accounts.service", "ActiveState") == "inactive" && property("vc-workspace-accounts.service", "Result") == "success"
	})
	t.Log("reproduced read-only /etc failure with a live expired account; restoring canonical permissions let the timer finish exact cleanup without manual reconciliation")
	state := exec("cat /var/lib/vc-workspace/agent-state.json")
	var heartbeat struct {
		Time int64 `json:"heartbeat_unix"`
	}
	if json.Unmarshal([]byte(state), &heartbeat) != nil || time.Now().Unix()-heartbeat.Time > 25 {
		t.Fatal("readiness heartbeat stopped during lifecycle recovery")
	}

	// A root sesexec may outlive the SCP client which requested its login.
	// Hold an actual PAM authentication after production registration, then
	// crash only the Guest dispatcher. This does not use a fake process or
	// manually reconcile to satisfy the final closure assertion.
	interruptedLogin := func(index int, checkpoint string) {
		t.Helper()
		if err := fixture(t.Context(), "stop"); err != nil {
			t.Fatal(err)
		}
		third := allocate(index)
		birthUser = third.Identity.Username
		birthCheckpoint = checkpoint
		if stage, err := birthFixture(t.Context(), "setup"); err != nil || stage != "armed" {
			t.Fatal("could not arm exact paused login", err)
		}
		loginResult := make(chan error, 1)
		go func() {
			_, err := executor.StartAgentSession(t.Context(), machine, third)
			loginResult <- err
		}()
		wait(15*time.Second, func() bool {
			stage, err := birthFixture(t.Context(), "crash")
			if err != nil {
				t.Fatal(err)
			}
			return stage == "crashed"
		})
		select {
		case err := <-loginResult:
			if err == nil {
				t.Fatal("killed Guest login returned success")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("killed Guest login transport did not settle")
		}
		value, err := executor.InspectAgentAccount(t.Context(), machine, birthUser)
		if err != nil || !value.Matches(third, "opening") || value.LoginWritersAbsent == nil || *value.LoginWritersAbsent || value.LoginStopped() {
			t.Fatal("pending login falsely acknowledged an undrained process domain", err)
		}
		if _, err := executor.StartAgentSession(t.Context(), machine, third); err == nil {
			t.Fatal("interrupted login version was replayed")
		}
		value, err = executor.InspectAgentAccount(t.Context(), machine, birthUser)
		if err != nil || !value.Matches(third, "opening") || value.LoginWritersAbsent == nil || *value.LoginWritersAbsent {
			t.Fatal("rejected login replay changed the original pending writer", err)
		}
		exec("systemctl start vc-workspace-agent.service")
		wait(40*time.Second, func() bool { return closed(third) })
		if stage, err := birthFixture(t.Context(), "resume"); err != nil || stage != "domain_drained_before_late_setuid" {
			t.Fatal("PAM root descendants survived the production reaper", err)
		}
		if !closed(third) {
			t.Fatal("late PAM completion resurrected a closed desktop")
		}
		// Reuse the immutable UID under a genuinely new lease, never reopen the
		// interrupted lease by merely incrementing its login generation.
		fresh := third
		fresh.LeaseID += "_next"
		fresh.ControlEpoch++
		fresh.LoginGeneration++
		fresh.ExpiresUnixSeconds = time.Now().Add(55 * time.Second).Unix()
		owned[len(owned)-1] = fresh
		target, err := executor.StartAgentSession(t.Context(), machine, fresh)
		if err != nil || target.UID != third.Identity.UID {
			t.Fatal("same immutable identity did not recover under a fresh lease", err)
		}
		if _, err := executor.StartAgentSession(t.Context(), machine, third); err == nil {
			t.Fatal("old login changed the recovered desktop")
		}
		value, err = executor.InspectAgentAccount(t.Context(), machine, birthUser)
		if err != nil || !value.Matches(fresh, "sealed") || value.LoginWritersAbsent == nil || *value.LoginWritersAbsent {
			t.Fatal("old login damaged fresh sealed session", err)
		}
		if err := executor.StopAgentSession(t.Context(), machine, fresh); err != nil {
			t.Fatal("recovered login could not be explicitly closed", err)
		}
		if _, err := birthFixture(t.Context(), "restore"); err != nil {
			t.Fatal("paused login fixture did not restore", err)
		}
		birthUser, birthCheckpoint = "", ""
		t.Logf("actual PAM paused at %s: login domain remained observable after dispatcher failure; canonical timer killed root creator and delayed root descendants before setuid; fresh lease recovered the same UID", checkpoint)
	}
	interruptedLogin(2, "before_auth")
	interruptedLogin(3, "after_auth")
	interruptedLogin(4, "orphan_after_auth")
}
