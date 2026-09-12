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
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

//go:embed guest_login_receipt_fixture.py
var guestLoginReceiptFixture string

//go:embed guest_logind_queue_fixture.py
var guestLogindQueueFixture string

func TestLiveLinuxLoginCreationReceipt(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_LOGIN_RECEIPT_AUDIT") != "true" {
		t.Skip("explicit isolated login receipt acceptance required")
	}
	liveLinuxLoginReceipt(t, guestLoginReceiptFixture, false)
}

func TestLiveLinuxQueuedLoginAndLogindRecovery(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_LOGIN_QUEUE_AUDIT") != "true" {
		t.Skip("explicit isolated logind pause/restart acceptance required")
	}
	liveLinuxLoginReceipt(t, guestLogindQueueFixture, true)
}

func liveLinuxLoginReceipt(t *testing.T, script string, queued bool) {
	t.Helper()
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
			t.Fatalf("receipt fixture: exit=%d stderr=%s transport=%v", result.ExitCode, result.Stderr, err)
		}
		return strings.TrimSpace(result.Stdout)
	}
	exec("set -eu\ntest \"$(. /etc/os-release; echo $VERSION_ID)\" = 13\ntest -z \"$(ps -C Xorg -C xrdp-sesexec -o pid=)\"\ntest -z \"$(getent -s files passwd | awk -F: '$1 ~ /^vca/ {print $1}')\"")
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	marker := "svc" + hex.EncodeToString(nonce[:])
	t.Logf("isolated logind receipt fixture %s on VM %d", marker, vmid)
	units := map[string]string{}
	for _, name := range []string{"vc-workspace-agent.service", "vc-workspace-accounts.service", "vc-workspace-accounts.timer"} {
		raw, err := linuxguest.Units.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		units[name] = string(raw)
	}
	service := func(ctx context.Context, op string) error {
		input, _ := json.Marshal(map[string]any{"units": units, "login_fence_installer": linuxguest.LoginFenceInstaller})
		result, err := client.ExecGuestWithInput(ctx, machine.Node, vmid, []string{"/usr/bin/python3", "-c", guestServiceFixture, op, marker}, input)
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("service %s: exit=%d stderr=%s transport=%v", op, result.ExitCode, result.Stderr, err)
		}
		return nil
	}
	executor := computer.NewPVEExecutor(client)
	name := store.AgentGuestUsername(marker)
	var lease computer.AccountLease
	var login chan error
	var loginCancel context.CancelFunc
	loginSettled := false
	receipt := func(ctx context.Context, op string) (string, error) {
		result, err := client.ExecGuestWithInput(ctx, machine.Node, vmid, []string{"/usr/bin/python3", "-c", script, op, marker, name, strconv.FormatInt(int64(lease.Identity.UID), 10)}, []byte(script))
		if err != nil || result.ExitCode != 0 {
			return "", fmt.Errorf("receipt %s: exit=%d stderr=%s transport=%v", op, result.ExitCode, result.Stderr, err)
		}
		var value struct{ Stage, Error string }
		if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil || value.Stage == "" {
			return "", fmt.Errorf("incomplete receipt %s", op)
		}
		if queued && op == "release" {
			if value.Error == "" {
				return "", fmt.Errorf("queued login rejection was not observed")
			}
			t.Logf("logind's actual reply to the queued dead caller: %s", value.Error)
		}
		return value.Stage, nil
	}
	t.Cleanup(func() {
		if loginCancel != nil {
			defer loginCancel()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		if err := service(ctx, "stop"); err != nil {
			t.Error(err)
			return
		}
		if lease.Identity.UID != 0 {
			if _, err := receipt(ctx, "restore"); err != nil {
				t.Error(err)
				return
			}
			// Restoring a fault may release an already accepted login. Observe
			// that original dispatch before attempting the exact-version stop;
			// canceling its QGA poll is not evidence the Guest writer exited.
			if login != nil && !loginSettled {
				select {
				case <-login:
					loginSettled = true
				case <-ctx.Done():
					t.Error("retaining unresolved in-flight login")
					return
				}
			}
			if err := executor.StopAgentSession(ctx, machine, lease); err != nil {
				t.Error("retaining unresolved owned identity", err)
				return
			}
			if login != nil {
				// Killing this test UID's user manager may leave only a failed
				// unit status. Clear that owned diagnostic after exact closure,
				// so a later disposable user reusing the UID starts from idle.
				if err := clearLiveLinuxAccountFailures(ctx, client, machine.Node, vmid, lease.Identity); err != nil {
					t.Error(err)
					return
				}
			}
			result, err := client.ExecGuest(ctx, machine.Node, vmid, []string{"/bin/sh", "-c", fmt.Sprintf("set -eu\ntest \"$(getent -s files passwd %s | cut -d: -f3)\" = %d\nuserdel -r %s\nrm -rf -- /var/lib/vc-workspace/computer-v2/users/%s\n! getent -s files passwd %s", name, lease.Identity.UID, name, name, name)})
			if err != nil || result.ExitCode != 0 {
				t.Error("account cleanup failed", err, result.ExitCode)
				return
			}
		}
		if err := service(ctx, "restore"); err != nil {
			t.Error(err)
		}
	})
	if err := service(t.Context(), "setup"); err != nil {
		t.Fatal(err)
	}
	if err := service(t.Context(), "stop"); err != nil {
		t.Fatal(err)
	}
	exec("! getent passwd " + name + " && test ! -e /home/" + name + " && test ! -e /var/lib/vc-workspace/computer-v2/users/" + name)
	identity, err := executor.PrepareAgentAccount(t.Context(), machine, name)
	if err != nil {
		t.Fatal(err)
	}
	lease = computer.AccountLease{SchemaVersion: 1, Identity: identity, LeaseID: "lease_" + marker, ControlEpoch: 1, LoginGeneration: 1, ExpiresUnixSeconds: time.Now().Add(90 * time.Second).Unix()}
	if stage, err := receipt(t.Context(), "setup"); err != nil || stage != "armed" {
		t.Fatal("arm receipt delay", stage, err)
	}
	login = make(chan error, 1)
	loginContext, cancelLogin := context.WithTimeout(context.WithoutCancel(t.Context()), 45*time.Second)
	loginCancel = cancelLogin
	go func() { _, err := executor.StartAgentSession(loginContext, machine, lease); login <- err }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		stage, err := receipt(t.Context(), "probe")
		if err != nil {
			t.Fatal(err)
		}
		if stage == "pending" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("user-manager delay was not reached")
		}
		time.Sleep(200 * time.Millisecond)
	}
	select {
	case err := <-login:
		loginSettled = true
		if err == nil {
			t.Fatal("unconfirmed logind creation returned login success")
		}
	case <-time.After(45 * time.Second):
		t.Fatal("lost create receipt did not settle")
	}
	if stage, err := receipt(t.Context(), "probe"); err != nil || stage != "pending" {
		t.Fatal("pending root job was not preserved", stage, err)
	}
	value, err := executor.InspectAgentAccount(t.Context(), machine, name)
	if queued {
		if err == nil {
			t.Fatal("paused logind must not produce a complete account observation")
		}
		t.Log("actual PAM request observed on D-Bus while logind was paused; timed-out caller exited; unavailable daemon was not treated as closure")
	} else {
		if err != nil || value.LoginWritersAbsent == nil || *value.LoginWritersAbsent || value.LoginStopped() {
			t.Fatal("unconfirmed logind job falsely acknowledged as closed", err)
		}
		t.Log("CreateSession timed out while a real root user-manager start job remained; closure was not acknowledged")
	}
	if stage, err := receipt(t.Context(), "release"); err != nil || stage != "released" {
		t.Fatal("release owned delay", stage, err)
	}
	exec("systemctl start vc-workspace-agent.service")
	deadline = time.Now().Add(50 * time.Second)
	for {
		value, err := executor.InspectAgentAccount(t.Context(), machine, name)
		if err == nil && value.LoginStopped() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canonical timer did not recover after manager job settled", err)
		}
		time.Sleep(time.Second)
	}
	lease.LeaseID += "_next"
	lease.ControlEpoch++
	lease.LoginGeneration++
	lease.ExpiresUnixSeconds = time.Now().Add(55 * time.Second).Unix()
	target, err := executor.StartAgentSession(t.Context(), machine, lease)
	if err != nil || target.UID != identity.UID {
		t.Fatal("fresh lease did not recover the same identity", err)
	}
	t.Log("normal user-manager startup resumed; canonical timer converged; fresh real desktop recovered the same UID")
	if queued {
		if stage, err := receipt(t.Context(), "restart"); err != nil || stage != "restarted" {
			t.Fatal("owned logind crash did not recover", stage, err)
		}
		after, err := executor.DiscoverSession(t.Context(), machine, name)
		if err != nil || after != target {
			t.Fatal("logind restart changed the live interactive Helper", err)
		}
		value, err = executor.InspectAgentAccount(t.Context(), machine, name)
		if err != nil || !value.Matches(lease, "sealed") || value.LoginWritersAbsent == nil || *value.LoginWritersAbsent {
			t.Fatal("logind restart lost the active exact login domain", err)
		}
		t.Log("logind SIGKILL recovered with a new daemon incarnation; original UID/session/Helper and sealed version remained bound")
	}
}
