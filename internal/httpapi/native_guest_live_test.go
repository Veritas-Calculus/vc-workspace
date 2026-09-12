package httpapi

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	linuxguest "github.com/Veritas-Calculus/vc-workspace/deploy/guest/linux"
	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

//go:embed native_guest_fixture.py
var nativeGuestFixture string

// Real RDP/PAM/XFCE and the shipped expiry timer. This deliberately does not
// claim default HTTP/macOS UI acceptance or modify an existing user's desktop.
func TestLiveLinuxNativeDesktopRetention(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_NATIVE_DESKTOP_AUDIT") != "true" {
		t.Skip("explicit isolated Native desktop acceptance required")
	}
	// A single known disposable desktop: never accept the business VM 158.
	if os.Getenv("VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID") != "160" {
		t.Fatal("explicit isolated VM 160 required")
	}
	client := livePrivilegeClient(t)
	machine := liveMachine(t, client, 160)
	if machine.Node != "infra-node1" || machine.Status != "running" || machine.Template || !isManagedDesktop(machine) || !strings.HasSuffix(machine.Name, "-check") {
		t.Fatal("idle isolated acceptance machine required")
	}
	qga := func(ctx context.Context, script string) (string, error) {
		result, err := client.ExecGuest(ctx, machine.Node, machine.VMID, []string{"/bin/sh", "-c", script})
		if err != nil || result.ExitCode != 0 {
			return "", fmt.Errorf("Native acceptance command exit=%d transport=%v stderr=%s", result.ExitCode, err, result.Stderr)
		}
		return strings.TrimSpace(result.Stdout), nil
	}
	preflight, err := qga(t.Context(), "set -eu\ntest \"$(. /etc/os-release; echo $VERSION_ID)\" = 13\ntest -z \"$(pgrep -x Xorg || true)\"\ntest -z \"$(pgrep -x xrdp-sesexec || true)\"\nsha256sum /etc/xrdp/xrdp.ini /etc/xrdp/sesman.ini /etc/pam.d/xrdp-sesman")
	if err != nil {
		t.Fatal(err)
	}
	address, err := netip.ParseAddr(os.Getenv("VC_WORKSPACE_LIVE_NATIVE_DESKTOP_ADDRESS"))
	if err != nil || !address.Is4() || !address.IsPrivate() {
		t.Fatal("explicit private Guest address required")
	}
	actual, err := qga(t.Context(), "ip -4 -o addr show scope global")
	if err != nil || !strings.Contains(actual, " "+address.String()+"/") {
		t.Fatal("address does not belong to acceptance Guest")
	}
	fingerprint, err := qga(t.Context(), "openssl x509 -in /etc/xrdp/cert.pem -noout -fingerprint -sha256")
	if err != nil {
		t.Fatal(err)
	}
	_, fingerprint, ok := strings.Cut(fingerprint, "=")
	if !ok {
		t.Fatal("certificate fingerprint missing")
	}
	fingerprint = strings.ReplaceAll(fingerprint, ":", "")
	if bytes, err := hex.DecodeString(fingerprint); err != nil || len(bytes) != 32 {
		t.Fatal("invalid certificate fingerprint")
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	marker := "svc" + hex.EncodeToString(random[:])
	username := store.NativeGuestUsername(marker)
	t.Logf("Native RDP fixture %s on VM 160, username %s", marker, username)
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
		result, err := client.ExecGuestWithInput(ctx, machine.Node, 160, []string{"/usr/bin/python3", "-c", guestServiceFixture, operation, marker, "native"}, payload)
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("Native service fixture %s exit=%d transport=%v stderr=%s", operation, result.ExitCode, err, result.Stderr)
		}
		return nil
	}
	accountFixture := func(ctx context.Context, operation string, extra ...string) (string, error) {
		args := append([]string{"/usr/bin/python3", "-c", nativeGuestFixture, operation, marker, username}, extra...)
		result, err := client.ExecGuest(ctx, machine.Node, 160, args)
		if err != nil || result.ExitCode != 0 {
			return "", fmt.Errorf("Native account fixture %s exit=%d transport=%v stderr=%s", operation, result.ExitCode, err, result.Stderr)
		}
		return result.Stdout, nil
	}
	executor := computer.NewPVEExecutor(client)
	var intent computer.NativeCredential
	var bound bool
	var transports []func()
	t.Cleanup(func() {
		for _, stop := range transports {
			stop()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
		defer cancel()
		// Drain the fixture's periodic writer before deleting its fixed identity;
		// it must not outlive removal and observe a later account reusing the UID.
		if err := fixture(ctx, "stop"); err != nil {
			t.Error(err)
			return
		}
		if intent.Identity.UID != 0 {
			observed, err := executor.InspectNativeAccount(ctx, machine, username)
			if err != nil || observed.Identity != intent.Identity {
				t.Error("retaining uncertain Native fixture identity", err)
				return
			}
			if !bound {
				if _, err := accountFixture(ctx, "bind"); err != nil {
					t.Error(err)
					return
				}
				bound = true
			}
			if observed.Lifecycle != nil && observed.Lifecycle.Revision > intent.Revision {
				intent.Revision = observed.Lifecycle.Revision
			}
			intent.Revision++
			intent.ExpiresUnixSeconds = 0
			intent.Phase = ""
			if err := executor.ChangeNativeCredential(ctx, machine, intent, "revoke", nil); err != nil {
				t.Error("retaining exact Native cleanup intent", err)
				return
			}
			if _, err := accountFixture(ctx, "remove"); err != nil {
				t.Error(err)
				return
			}
		}
		if err := fixture(ctx, "restore"); err != nil {
			t.Error(err)
			return
		}
		after, err := qga(ctx, "sha256sum /etc/xrdp/xrdp.ini /etc/xrdp/sesman.ini /etc/pam.d/xrdp-sesman")
		if err != nil || after != preflight {
			t.Error("xrdp/PAM configuration was not restored", err)
		}
	})
	if err := fixture(t.Context(), "setup"); err != nil {
		t.Fatal(err)
	}
	if _, err := accountFixture(t.Context(), "reserve"); err != nil {
		t.Fatal(err)
	}
	identity, err := executor.PrepareNativeAccount(t.Context(), machine, username)
	if err != nil {
		t.Fatal(err)
	}
	intent = computer.NativeCredential{SchemaVersion: 1, Identity: identity, ConnectionID: "conn_" + marker, Revision: 1, ExpiresUnixSeconds: time.Now().Add(4 * time.Minute).Unix()}
	if _, err := accountFixture(t.Context(), "bind"); err != nil {
		t.Fatal(err)
	}
	bound = true
	password := func() []byte {
		var v [24]byte
		if _, err := rand.Read(v[:]); err != nil {
			t.Fatal(err)
		}
		return []byte(hex.EncodeToString(v[:]))
	}
	issue := func() []byte {
		t.Helper()
		secret := password()
		if err := executor.ChangeNativeCredential(t.Context(), machine, intent, "issue", secret); err != nil {
			clear(secret)
			t.Fatal(err)
		}
		return secret
	}
	desktop := func() string {
		t.Helper()
		until := time.Now().Add(45 * time.Second)
		for time.Now().Before(until) {
			value, err := accountFixture(t.Context(), "desktop")
			if err == nil {
				var v struct {
					UID     uint32          `json:"uid"`
					PID     int             `json:"pid"`
					Ticks   string          `json:"ticks"`
					Journal json.RawMessage `json:"journal"`
				}
				if json.Unmarshal([]byte(value), &v) == nil && v.UID == identity.UID && v.PID > 1 && len(v.Journal) > 2 {
					return fmt.Sprintf("%d:%s", v.PID, v.Ticks)
				}
			}
			select {
			case <-t.Context().Done():
				t.Fatal("desktop wait canceled")
			case <-time.After(time.Second):
			}
		}
		t.Fatal("real XFCE/PAM desktop did not become ready")
		return ""
	}
	var currentXorg int
	connect := func(index int, secret []byte) func() {
		t.Helper()
		if _, err := accountFixture(t.Context(), "checkpoint", fmt.Sprint(index)); err != nil {
			clear(secret)
			t.Fatal(err)
		}
		stop := startNativeAcceptanceRDP(t, marker, index, address.String(), username, string(secret), fingerprint)
		clear(secret)
		transports = append(transports, stop)
		until := time.Now().Add(40 * time.Second)
		for {
			if receipt, err := accountFixture(t.Context(), "connected", fmt.Sprint(index)); err == nil {
				var peer struct {
					UID  uint32 `json:"uid"`
					Xorg int    `json:"xorg_pid"`
					RDP  int    `json:"rdp_pid"`
				}
				if json.Unmarshal([]byte(receipt), &peer) != nil || peer.UID != identity.UID || peer.Xorg <= 1 || peer.RDP <= 1 {
					t.Fatal("incomplete live RDP peer receipt")
				}
				currentXorg = peer.Xorg
				break
			}
			if time.Now().After(until) {
				t.Fatal("fresh RDP transport did not reach the exact user's live Xorg")
			}
			select {
			case <-t.Context().Done():
				t.Fatal("RDP connection wait canceled")
			case <-time.After(time.Second):
			}
		}
		return stop
	}
	firstStop := connect(1, issue())
	firstXorg := currentXorg
	firstPID := desktop()
	observed, err := executor.InspectNativeAccount(t.Context(), machine, username)
	if err != nil || !observed.Matches(intent, "issued") || observed.LoginWritersAbsent == nil || *observed.LoginWritersAbsent || observed.ProcessesAbsent {
		t.Fatal("real Native login domain not retained", err)
	}
	intent.Revision++
	if err := executor.ChangeNativeCredential(t.Context(), machine, intent, "retire", nil); err != nil {
		t.Fatal(err)
	}
	if desktop() != firstPID {
		t.Fatal("retiring credentials destroyed the desktop")
	}
	firstStop()
	intent.Revision++
	intent.ConnectionID += "_next"
	secondStop := connect(2, issue())
	if desktop() != firstPID || currentXorg != firstXorg {
		t.Fatal("Native RDP reconnect replaced the user's desktop")
	}
	t.Log("actual RDP/PAM login, credential retirement and reconnect retained the same XFCE PID/start identity")
	intent.Revision++
	intent.ExpiresUnixSeconds = 0
	if err := executor.ChangeNativeCredential(t.Context(), machine, intent, "revoke", nil); err != nil {
		t.Fatal(err)
	}
	observed, err = executor.InspectNativeAccount(t.Context(), machine, username)
	if err != nil || !observed.Matches(intent, "revoked") || !observed.Closed() {
		t.Fatal("Native revoke did not close the real login domain", err)
	}
	if _, err := accountFixture(t.Context(), "nodes-absent"); err != nil {
		t.Fatal(err)
	}
	secondStop()
	intent.Revision++
	intent.ConnectionID += "_expiry"
	intent.ExpiresUnixSeconds = time.Now().Add(50 * time.Second).Unix()
	thirdStop := connect(3, issue())
	if desktop() == firstPID {
		t.Fatal("revoked desktop survived a fresh login")
	}
	if _, err := accountFixture(t.Context(), "freeze-desktop"); err != nil {
		t.Fatal(err)
	}
	observed, err = executor.InspectNativeAccount(t.Context(), machine, username)
	if err != nil || observed.ProcessesAbsent || observed.LoginWritersAbsent == nil || *observed.LoginWritersAbsent {
		t.Fatal("frozen Xorg/login domain was mistaken for closure", err)
	}
	until := time.Now().Add(75 * time.Second)
	for {
		observed, err = executor.InspectNativeAccount(t.Context(), machine, username)
		revoked := intent
		revoked.ExpiresUnixSeconds = 0
		if err == nil && observed.Matches(revoked, "revoked") && observed.Closed() {
			break
		}
		if time.Now().After(until) {
			t.Fatal("shipped local expiry timer did not close connected Native RDP", err)
		}
		select {
		case <-t.Context().Done():
			t.Fatal("expiry wait canceled")
		case <-time.After(time.Second):
		}
	}
	thirdStop()
	if _, err := accountFixture(t.Context(), "nodes-absent"); err != nil {
		t.Fatal(err)
	}
	t.Log("explicit revoke and original short deadline closed real Native XFCE/root login writers; expiry used shipped timer, no manual reconcile or clock/journal edits")
}

// The existing disposable FreeRDP/Xvfb image uses stdin-only arguments and an
// explicit certificate pin. It is an RDP client fixture, not the macOS app.
func startNativeAcceptanceRDP(t *testing.T, marker string, index int, address, username, password, fingerprint string) func() {
	t.Helper()
	name := fmt.Sprintf("vcw-native-rdp-%s-%d", marker, index)
	args := []string{"run", "--rm", "--name", name, "--label", "vc-workspace.test-instance=" + marker, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,nosuid,nodev", "--tmpfs", "/run:rw,nosuid,nodev", "--tmpfs", "/root:rw,nosuid,nodev,mode=700", "-i", "vc-workspace-windows-session-test:local", "/args-from:stdin"}
	command := exec.Command("docker", args...)
	command.Stdin = strings.NewReader(strings.Join([]string{"/v:" + address + ":3389", "/u:" + username, "/p:" + password, "/cert:fingerprint:sha256:" + fingerprint, "/size:1280x720", "-clipboard", "/log-level:OFF"}, "\n") + "\n")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			label, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "vc-workspace.test-instance"}}`, name).Output()
			if err == nil {
				if strings.TrimSpace(string(label)) != marker {
					t.Error("refuse unrelated RDP container cleanup")
					return
				}
				if err := exec.CommandContext(ctx, "docker", "stop", "-t", "3", name).Run(); err != nil {
					t.Error("RDP container stop failed", err)
				}
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Error("RDP client cleanup timed out")
				return
			}
			if exec.CommandContext(ctx, "docker", "inspect", name).Run() == nil {
				t.Error("RDP fixture remains")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}
