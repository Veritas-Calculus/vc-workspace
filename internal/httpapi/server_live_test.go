package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

// This destructive opt-in check toggles the real desktop account through the
// exact policy commands used by the server, verifies both states, and restores
// the original state. It may sign out an active vdi session during downgrade.
func TestLiveLinuxDesktopPrivilegePolicy(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_PRIVILEGE_AUDIT") != "true" {
		t.Skip("set VC_WORKSPACE_LIVE_PRIVILEGE_AUDIT=true and VC_WORKSPACE_LIVE_PRIVILEGE_VMID to test a real Linux desktop")
	}
	vmid, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_PRIVILEGE_VMID")))
	if err != nil || vmid <= 0 {
		t.Fatal("VC_WORKSPACE_LIVE_PRIVILEGE_VMID must be a positive VMID")
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

// This opt-in check changes xrdp channel policy on an explicit Linux desktop,
// verifies both allow and deny states, and restores the exact original config.
// Restarting xrdp will disconnect an active remote session.
func TestLiveLinuxDesktopSessionPolicy(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_SESSION_POLICY_AUDIT") != "true" {
		t.Skip("set VC_WORKSPACE_LIVE_SESSION_POLICY_AUDIT=true and VC_WORKSPACE_LIVE_SESSION_POLICY_VMID to test a real Linux desktop")
	}
	vmid, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_SESSION_POLICY_VMID")))
	if err != nil || vmid <= 0 {
		t.Fatal("VC_WORKSPACE_LIVE_SESSION_POLICY_VMID must be a positive VMID")
	}
	client := livePrivilegeClient(t)
	machine := liveMachine(t, client, vmid)
	if machine.Kind != "qemu" || machine.Template || machine.Status != "running" || !isManagedDesktop(machine) {
		t.Fatalf("VMID %d must be a running managed Linux desktop: %#v", vmid, machine)
	}
	configuration, err := client.VMConfiguration(t.Context(), machine.Node, machine.VMID)
	if err != nil {
		t.Fatal(err)
	}
	if desktopOSFamily(configuration.OSType) != "linux" {
		t.Fatalf("VMID %d is not a Linux desktop", vmid)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	backup := "/var/tmp/vc-workspace-policy-live-" + suffix
	backupCommand := fmt.Sprintf(`set -eu
backup=%s
install -d -m 0700 "$backup"
cp /etc/xrdp/xrdp.ini "$backup/xrdp.ini"
cp /etc/xrdp/sesman.ini "$backup/sesman.ini"
if [ -e /etc/vc-workspace/background-policy ]; then
  cp /etc/vc-workspace/background-policy "$backup/background-policy"
else
  : >"$backup/no-background-policy"
fi`, backup)
	result, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, []string{"/bin/sh", "-c", backupCommand})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("back up Guest session policy: result=%#v error=%v", result, err)
	}
	defer func() {
		restoreCommand := fmt.Sprintf(`set -eu
backup=%s
cp "$backup/xrdp.ini" /etc/xrdp/xrdp.ini
cp "$backup/sesman.ini" /etc/xrdp/sesman.ini
if [ -e "$backup/no-background-policy" ]; then
  rm -f /etc/vc-workspace/background-policy
else
  install -d -m 0755 /etc/vc-workspace
  cp "$backup/background-policy" /etc/vc-workspace/background-policy
fi
systemctl restart xrdp-sesman.service xrdp.service
rm -rf "$backup"`, backup)
		restoreResult, restoreErr := client.ExecGuest(context.Background(), machine.Node, machine.VMID, []string{"/bin/sh", "-c", restoreCommand})
		if restoreErr != nil || restoreResult.ExitCode != 0 {
			t.Errorf("restore Guest session policy: result=%#v error=%v", restoreResult, restoreErr)
		}
	}()

	for _, test := range []struct {
		name      string
		clipboard bool
		drive     bool
		wantClip  string
		wantDrive string
		wantLimit string
	}{
		{name: "allow", clipboard: true, drive: true, wantClip: "true", wantDrive: "true", wantLimit: "none"},
		{name: "deny", clipboard: false, drive: false, wantClip: "false", wantDrive: "false", wantLimit: "all"},
	} {
		command, err := desktopSessionPolicyCommand("linux", store.DesktopAccessPolicy{
			ClipboardRedirection: test.clipboard,
			DriveRedirection:     test.drive,
			ManagedBackground:    false,
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, command)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("apply %s session policy: result=%#v error=%v", test.name, result, err)
		}
		verify := fmt.Sprintf(`set -eu
grep -Eq '^cliprdr=%s$' /etc/xrdp/xrdp.ini
grep -Eq '^rdpdr=%s$' /etc/xrdp/xrdp.ini
grep -Eq '^RestrictInboundClipboard=%s$' /etc/xrdp/sesman.ini
grep -Eq '^RestrictOutboundClipboard=%s$' /etc/xrdp/sesman.ini
grep -Eq '^EnableFuseMount=%s$' /etc/xrdp/sesman.ini
grep -Eq '^allow-user$' /etc/vc-workspace/background-policy
systemctl is-active --quiet xrdp.service xrdp-sesman.service`, test.wantClip, test.wantDrive, test.wantLimit, test.wantLimit, test.wantDrive)
		verified, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, []string{"/bin/sh", "-c", verify})
		if err != nil || verified.ExitCode != 0 {
			t.Fatalf("verify %s session policy: result=%#v error=%v", test.name, verified, err)
		}
	}
}

// This read-only opt-in check verifies the final managed Linux desktop policy
// after it has travelled through the Web API, reconciliation, QGA and Guest
// enforcement path. Unlike TestLiveLinuxDesktopSessionPolicy, it does not
// modify or restore Guest configuration.
func TestLiveLinuxDesktopManagedPolicyState(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_SESSION_POLICY_VERIFY") != "true" {
		t.Skip("set VC_WORKSPACE_LIVE_SESSION_POLICY_VERIFY=true and VC_WORKSPACE_LIVE_SESSION_POLICY_VMID to verify a real Linux desktop")
	}
	vmid, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_SESSION_POLICY_VMID")))
	if err != nil || vmid <= 0 {
		t.Fatal("VC_WORKSPACE_LIVE_SESSION_POLICY_VMID must be a positive VMID")
	}
	client := livePrivilegeClient(t)
	machine := liveMachine(t, client, vmid)
	if machine.Kind != "qemu" || machine.Template || machine.Status != "running" || !isManagedDesktop(machine) {
		t.Fatalf("VMID %d must be a running managed Linux desktop: %#v", vmid, machine)
	}
	configuration, err := client.VMConfiguration(t.Context(), machine.Node, machine.VMID)
	if err != nil {
		t.Fatal(err)
	}
	if desktopOSFamily(configuration.OSType) != "linux" {
		t.Fatalf("VMID %d is not a Linux desktop", vmid)
	}

	verify := `set -eu
grep -Eq '^cliprdr=true$' /etc/xrdp/xrdp.ini
grep -Eq '^rdpdr=false$' /etc/xrdp/xrdp.ini
grep -Eq '^RestrictInboundClipboard=none$' /etc/xrdp/sesman.ini
grep -Eq '^RestrictOutboundClipboard=none$' /etc/xrdp/sesman.ini
grep -Eq '^EnableFuseMount=false$' /etc/xrdp/sesman.ini
grep -Eq '^managed$' /etc/vc-workspace/background-policy
test -r /usr/share/backgrounds/vc-workspace/desktop-background.png || test -r /usr/share/backgrounds/vc-workspace/desktop-background-session.jpg
test -x /usr/local/bin/vc-workspace-apply-background
test -r /etc/xdg/autostart/vc-workspace-background.desktop
test ! -e /tmp/vc-workspace-desktop-background-session.jpg
systemctl is-active --quiet xrdp.service xrdp-sesman.service`
	result, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, []string{"/bin/sh", "-c", verify})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("verify managed session policy: result=%#v error=%v", result, err)
	}
	t.Logf("verified managed background, clipboard and drive policy on VMID %d", vmid)
}

func TestLiveWindowsDesktopPrivilegePolicy(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_PRIVILEGE_AUDIT") != "true" {
		t.Skip("set VC_WORKSPACE_LIVE_WINDOWS_PRIVILEGE_AUDIT=true and VC_WORKSPACE_LIVE_PRIVILEGE_VMID to test a real Windows desktop")
	}
	vmid, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_PRIVILEGE_VMID")))
	if err != nil || vmid <= 0 {
		t.Fatal("VC_WORKSPACE_LIVE_PRIVILEGE_VMID must be a positive VMID")
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

// This destructive opt-in check provisions a unique per-platform-user Guest
// account on an explicit Linux acceptance VM, rotates two connection
// credentials, exercises idempotent release, and removes the temporary account.
// The database must be disposable and use the vc_workspace_identity_live_ prefix.
func TestLiveNativePerUserIdentityLifecycle(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_NATIVE_IDENTITY_AUDIT") != "true" {
		t.Skip("set VC_WORKSPACE_LIVE_NATIVE_IDENTITY_AUDIT=true to test a real per-user Guest identity")
	}
	vmid, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_NATIVE_IDENTITY_VMID")))
	if err != nil || vmid <= 0 {
		t.Fatal("VC_WORKSPACE_LIVE_NATIVE_IDENTITY_VMID must be a positive VMID")
	}
	databaseURL := strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_DATABASE_URL"))
	parsedDatabaseURL, err := url.Parse(databaseURL)
	if err != nil || !strings.HasPrefix(strings.TrimPrefix(parsedDatabaseURL.Path, "/"), "vc_workspace_identity_live_") {
		t.Fatal("VC_WORKSPACE_LIVE_DATABASE_URL must identify a disposable vc_workspace_identity_live_* database")
	}

	client := livePrivilegeClient(t)
	machine := liveMachine(t, client, vmid)
	if machine.Kind != "qemu" || machine.Template || machine.Status != "running" || !isManagedDesktop(machine) {
		t.Fatalf("VMID %d must be a running managed non-template QEMU acceptance desktop: %#v", vmid, machine)
	}
	configuration, err := client.VMConfiguration(t.Context(), machine.Node, machine.VMID)
	if err != nil {
		t.Fatal(err)
	}
	if desktopOSFamily(configuration.OSType) != "linux" {
		t.Fatalf("VMID %d must be a Linux acceptance desktop, got %q", vmid, configuration.OSType)
	}

	database, err := store.Open(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	userID := "live-native-identity-" + suffix
	username := "native-" + suffix
	password := "Live-only-1!" + suffix
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	user, err := database.CreateLocalUser(t.Context(), store.User{ID: userID, Username: username, DisplayName: "Native identity acceptance", PasswordHash: passwordHash})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: vmid, DisplayName: machine.Name, Node: machine.Node, OSFamily: "linux"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateManagedDesktopIdentity(t.Context(), vmid, "personal", user.ID, ""); err != nil {
		t.Fatal(err)
	}

	guestUsername := managedGuestUsername(user.ID)
	t.Cleanup(func() {
		if !guestUsernamePattern.MatchString(guestUsername) {
			t.Errorf("refusing to clean invalid Guest username %q", guestUsername)
			return
		}
		cleanupContext, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		command := []string{"/bin/sh", "-c", fmt.Sprintf(`set -eu
username=%s
pkill -u "$username" >/dev/null 2>&1 || true
rm -f "/etc/sudoers.d/vc-workspace-$username"
userdel -r "$username" >/dev/null 2>&1 || userdel "$username" >/dev/null 2>&1 || true
if getent passwd "$username" >/dev/null; then exit 1; fi`, guestUsername)}
		result, cleanupErr := client.ExecGuest(cleanupContext, machine.Node, machine.VMID, command)
		if cleanupErr != nil || result.ExitCode != 0 {
			t.Errorf("remove temporary Guest identity: result=%#v error=%v", result, cleanupErr)
		}
	})

	server := httptest.NewServer(New(Dependencies{PVE: client, Store: database, PublicURL: "http://127.0.0.1"}).Handler())
	t.Cleanup(server.Close)
	loginBody, _ := json.Marshal(map[string]string{"username": username, "password": password})
	loginStatus, _, loginResponse := liveNativeIdentityRequest(t, server.Client(), http.MethodPost, server.URL+"/api/v1/auth/native/login", "", loginBody)
	if loginStatus != http.StatusCreated {
		t.Fatalf("native login returned %d: %s", loginStatus, strings.TrimSpace(string(loginResponse)))
	}
	var login struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(loginResponse, &login); err != nil || login.AccessToken == "" {
		t.Fatalf("decode native login: response=%s error=%v", strings.TrimSpace(string(loginResponse)), err)
	}

	first := createLiveNativeConnection(t, server.Client(), server.URL, login.AccessToken, vmid)
	second := createLiveNativeConnection(t, server.Client(), server.URL, login.AccessToken, vmid)
	if first.Username != guestUsername || second.Username != guestUsername || first.Username == "vdi" {
		t.Fatalf("connection did not use the stable per-user Guest identity: first=%q second=%q expected=%q", first.Username, second.Username, guestUsername)
	}
	if first.ID == second.ID || first.Password == second.Password || first.Password == "" || second.Password == "" {
		t.Fatal("successive connections did not rotate both the session and Guest credential")
	}
	probe, err := liveExecGuest(t, client, machine, []string{"/bin/sh", "-c", fmt.Sprintf(`id -u %s && id -nG %s`, guestUsername, guestUsername)})
	if err != nil || probe.ExitCode != 0 || !containsWord(probe.Stdout, "ssl-cert") {
		t.Fatalf("temporary Guest identity was not provisioned for RDP: result=%#v error=%v", probe, err)
	}

	for _, connectionID := range []string{first.ID, second.ID, second.ID} {
		status, _, body := liveNativeIdentityRequest(t, server.Client(), http.MethodDelete, server.URL+"/api/v1/native/connections/"+connectionID, login.AccessToken, nil)
		if status != http.StatusNoContent {
			t.Fatalf("release connection %s returned %d: %s", connectionID, status, strings.TrimSpace(string(body)))
		}
	}
	terminal, err := database.BeginRevokeDesktopConnection(t.Context(), second.ID, user.ID)
	if err != nil || terminal.State != "revoked" {
		t.Fatalf("connection did not reach revoked state: connection=%#v error=%v", terminal, err)
	}
	t.Logf("verified stable per-user Guest identity %s and idempotent credential revocation on VMID %d", guestUsername, vmid)
}

type liveNativeConnection struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func createLiveNativeConnection(t *testing.T, client *http.Client, serverURL, token string, vmid int) liveNativeConnection {
	t.Helper()
	status, headers, body := liveNativeIdentityRequest(t, client, http.MethodPost, fmt.Sprintf("%s/api/v1/native/desktops/%d/connections", serverURL, vmid), token, nil)
	if status != http.StatusCreated {
		t.Fatalf("create native connection returned %d: %s", status, strings.TrimSpace(string(body)))
	}
	if headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("connection response Cache-Control=%q, want no-store", headers.Get("Cache-Control"))
	}
	var connection liveNativeConnection
	if err := json.Unmarshal(body, &connection); err != nil || connection.ID == "" {
		t.Fatalf("decode native connection: response=%s error=%v", strings.TrimSpace(string(body)), err)
	}
	return connection
}

func liveNativeIdentityRequest(t *testing.T, client *http.Client, method, endpoint, token string, body []byte) (int, http.Header, []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header.Clone(), responseBody
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
	endpoint := strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_PVE_ENDPOINT"))
	credentialFile := strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE"))
	if endpoint == "" || credentialFile == "" {
		t.Fatal("VC_WORKSPACE_LIVE_PVE_ENDPOINT and VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE are required")
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
