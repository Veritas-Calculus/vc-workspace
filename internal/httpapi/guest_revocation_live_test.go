package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

// Opt-in: creates two disposable local accounts and harmless sleep processes on
// an explicit Linux acceptance VM. It does not restart xrdp or touch existing
// users. This verifies Guest account/process enforcement, not a visible RDP UI.
func TestLiveLinuxRetainedGuestRevocation(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_GUEST_REVOCATION") != "true" {
		t.Skip("set VC_WORKSPACE_LIVE_GUEST_REVOCATION=true and VC_WORKSPACE_LIVE_GUEST_REVOCATION_VMID")
	}
	vmid, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_GUEST_REVOCATION_VMID"))
	if err != nil || vmid <= 0 {
		t.Fatal("explicit positive acceptance VMID required")
	}
	client := livePrivilegeClient(t)
	machine := liveMachine(t, client, vmid)
	if machine.Kind != "qemu" || machine.Template || machine.Status != "running" || !isManagedDesktop(machine) {
		t.Fatal("target must be a running managed acceptance desktop")
	}
	configuration, err := client.VMConfiguration(t.Context(), machine.Node, vmid)
	if err != nil || desktopOSFamily(configuration.OSType) != "linux" {
		t.Fatalf("Linux target required: %v", err)
	}
	db, suffix := regressionDB(t) // isolated schema, never migrates the public schema
	user := store.User{ID: "revoke-live-" + suffix, Username: "revoke-live-" + suffix, DisplayName: "Revocation acceptance", PasswordHash: "unused"}
	if _, err := db.CreateLocalUser(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: vmid, Node: machine.Node, DisplayName: machine.Name, OSFamily: "linux"}); err != nil {
		t.Fatal(err)
	}
	username := managedGuestUsername(user.ID)
	control := managedGuestUsername("control-" + suffix)
	unit := "vcw-revocation-" + suffix
	controlUnit := unit + "-control"
	exec := func(script string) string {
		t.Helper()
		result, err := liveExecGuest(t, client, machine, []string{"/bin/sh", "-c", script})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("Guest assertion failed: exit=%d out=%s err=%s transport=%v", result.ExitCode, result.Stdout, result.Stderr, err)
		}
		return result.Stdout
	}
	checkAccount := func(name string, permitted bool) {
		t.Helper()
		// runuser is a privileged account switch, not an xrdp login. Exercise
		// the actual xrdp PAM account stack without printing a password/hash.
		expected := 13 // Linux-PAM PAM_ACCT_EXPIRED
		if permitted {
			expected = 0
		}
		exec(fmt.Sprintf(`python3 -c '
import ctypes,sys
lib=ctypes.CDLL("libpam.so.0")
callback=ctypes.CFUNCTYPE(ctypes.c_int,ctypes.c_int,ctypes.c_void_p,ctypes.c_void_p,ctypes.c_void_p)(lambda *args: 19)
class Conv(ctypes.Structure):
    _fields_=[("conv",ctypes.c_void_p),("data",ctypes.c_void_p)]
conv=Conv(ctypes.cast(callback,ctypes.c_void_p),None)
handle=ctypes.c_void_p()
result=lib.pam_start(b"xrdp-sesman",sys.argv[1].encode(),ctypes.byref(conv),ctypes.byref(handle))
if result: raise SystemExit(result)
result=lib.pam_acct_mgmt(handle,0)
lib.pam_end(handle,result)
print("xrdp_pam_account="+str(result))
expected=int(sys.argv[2])
# The common-account requisite pam_deny can normalize expired-account
# failure to PAM_AUTH_ERR (7); the control account must still return success.
raise SystemExit(0 if (result==0 if expected==0 else result in (7,13)) else 1)
' %s %d`, name, expected))
	}
	// Refuse collisions before registering cleanup; never delete a pre-existing account.
	exec(fmt.Sprintf("! getent passwd %s && ! getent passwd %s", username, control))
	t.Logf("temporary target=%s control=%s", username, control)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, name := range []string{username, control} {
			command, err := terminateGuestSessionCommand("linux", name)
			if err != nil {
				t.Error(err)
				continue
			}
			if result, err := client.ExecGuest(ctx, machine.Node, vmid, command); err != nil || result.ExitCode != 0 {
				t.Errorf("cleanup temporary account session: %v %v", result, err)
			}
		}
		command := fmt.Sprintf("systemctl stop %s.service %s.service >/dev/null 2>&1 || true\nif getent passwd %s >/dev/null; then userdel -r %s; fi\nif getent passwd %s >/dev/null; then userdel -r %s; fi\n! getent passwd %s && ! getent passwd %s", unit, controlUnit, username, username, control, control, username, control)
		if result, err := client.ExecGuest(ctx, machine.Node, vmid, []string{"/bin/sh", "-c", command}); err != nil || result.ExitCode != 0 {
			t.Errorf("remove temporary accounts: %v %v", result, err)
		}
	})
	prepare := func(name string) {
		t.Helper()
		command, err := ensureGuestUserCommand("linux", name)
		if err != nil {
			t.Fatal(err)
		}
		if result, err := liveExecGuest(t, client, machine, command); err != nil || result.ExitCode != 0 {
			t.Fatalf("prepare temporary account: %v %v", result, err)
		}
		password, err := auth.OpaqueToken(24)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.SetGuestUserPassword(t.Context(), machine.Node, vmid, name, "Vcw1!"+password); err != nil {
			t.Fatal(err)
		}
		command, err = enableGuestAccountCommand("linux", name)
		if err != nil {
			t.Fatal(err)
		}
		if result, err := liveExecGuest(t, client, machine, command); err != nil || result.ExitCode != 0 {
			t.Fatalf("activate temporary account: %v %v", result, err)
		}
		checkAccount(name, true)
	}
	prepare(control)
	exec(fmt.Sprintf("systemd-run --quiet --collect --unit=%s --uid=%s /bin/sleep 600", controlUnit, control))
	s := New(Dependencies{Store: db, PVE: client})
	for index, event := range []string{"assignment_removed", "native_logout"} {
		func() {
			ctx := t.Context()
			prepare(username)
			if _, err := db.PutDesktopAssignment(ctx, store.DesktopAssignment{SubjectType: "user", SubjectID: user.ID, DesktopVMID: vmid}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.PutGuestIdentityBinding(ctx, store.GuestIdentityBinding{DesktopVMID: vmid, UserID: user.ID, GuestUsername: username, State: "ready"}); err != nil {
				t.Fatal(err)
			}
			token := fmt.Sprintf("disposable-%s-%d", suffix, index)
			if err := db.CreateNativeSession(ctx, auth.TokenDigest(token), user.ID, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			connection, err := db.CreateNativeDesktopConnectionSession(ctx, store.DesktopConnectionSession{ID: fmt.Sprintf("connection-%d", index), UserID: user.ID, DesktopVMID: vmid, GuestUsername: username, ExpiresAt: time.Now().Add(time.Hour)}, auth.TokenDigest(token))
			if err != nil {
				t.Fatal(err)
			}
			exec(fmt.Sprintf("systemd-run --quiet --collect --unit=%s --uid=%s /bin/sleep 600\nsleep 1\npgrep -u %s >/dev/null", unit, username, username))
			r := httptest.NewRequest("DELETE", "/api/v1/native/connections/"+connection.ID, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != 204 {
				t.Fatalf("ordinary disconnect: %d %s", w.Code, w.Body)
			}
			exec(fmt.Sprintf("pgrep -u %s >/dev/null", username))
			if event == "assignment_removed" {
				if _, err := db.DeleteDesktopAssignment(ctx, "user", user.ID, vmid); err != nil {
					t.Fatal(err)
				}
			} else {
				r = httptest.NewRequest("POST", "/api/v1/auth/native/logout", nil)
				r.Header.Set("Authorization", "Bearer "+token)
				w = httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				if w.Code != 204 {
					t.Fatalf("logout: %d %s", w.Code, w.Body)
				}
			}
			s.revokeDueGuestIdentities(ctx)
			pending, err := db.PendingGuestIdentityRevocations(ctx, vmid)
			if err != nil || len(pending) != 0 {
				t.Fatalf("Guest cleanup not acknowledged: %v %v", pending, err)
			}
			exec(fmt.Sprintf(`set -eu
if pgrep -u %s >/dev/null; then echo target_process_survived; exit 1; fi
getent shadow %s | awk -F: '$2 ~ /^!/ && $8 == 1 {found=1} END {if (!found) print "account_not_locked_and_expired"; exit !found}'
pgrep -u %s >/dev/null || { echo control_process_missing; exit 1; }`, username, username, control))
			checkAccount(username, false)
			checkAccount(control, true)
			t.Logf("VM %d: retained process survived ordinary disconnect, %s disabled PAM account and killed only target processes; queue completed", vmid, event)
		}()
		if t.Failed() {
			return
		}
	}
	prepare(username)
	output := exec(fmt.Sprintf("getent shadow %s | awk -F: '$2 !~ /^!/ && $8 == \"\" {print \"restored\"}'", username))
	if !strings.Contains(output, "restored") {
		t.Fatal("reauthorization did not restore account")
	}
	t.Log("Guest account was re-enabled successfully; existing users and xrdp configuration unchanged")
}
