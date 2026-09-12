package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/agentapi"
	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/Veritas-Calculus/vc-workspace/internal/testguest"
)

type observedAgentSessionGuest struct {
	*pve.Client
	t *testing.T
}

func (g observedAgentSessionGuest) ExecGuest(ctx context.Context, node string, vmid int, args []string) (pve.GuestExecResult, error) {
	result, err := g.Client.ExecGuest(ctx, node, vmid, args)
	if len(args) > 1 && strings.HasPrefix(args[1], "computer-v2-") && args[1] != "computer-v2-dispatch" && args[1] != "computer-v2-session" && (err != nil || result.ExitCode != 0) {
		g.t.Logf("acceptance Guest %s exit=%d stderr=%s transport=%v", args[1], result.ExitCode, result.Stderr, err)
	}
	return result, err
}

func (g observedAgentSessionGuest) ExecGuestWithInput(ctx context.Context, node string, vmid int, args []string, input []byte) (pve.GuestExecResult, error) {
	result, err := g.Client.ExecGuestWithInput(ctx, node, vmid, args, input)
	// Only sesrun diagnostics from these new, empty test accounts. Never log
	// input bytes or Computer Use request/response contents.
	if len(args) > 0 && args[0] == "/usr/bin/xrdp-sesrun" && (err != nil || result.ExitCode != 0) {
		g.t.Logf("acceptance sesrun exit=%d out=%s stderr=%s transport=%v", result.ExitCode, result.Stdout, result.Stderr, err)
	}
	return result, err
}

// Installs only the new fixed v2 executable. Does not replace the old service,
// restart xrdp, change package repositories or log out any existing user.
func TestLiveInstallAgentSessionBinary(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_AGENT_SESSION_INSTALL") != "true" {
		t.Skip("explicit acceptance VM and local Linux amd64 artifact required")
	}
	vmid, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_AGENT_SESSION_VMID"))
	if err != nil || vmid <= 0 {
		t.Fatal("explicit positive acceptance VMID required")
	}
	path := os.Getenv("VC_WORKSPACE_LIVE_AGENT_SESSION_BINARY")
	if path == "" {
		t.Fatal("explicit local artifact required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 64 || len(data) > 32*1024*1024 || string(data[:4]) != "\x7fELF" || data[4] != 2 || data[5] != 1 || data[18] != 62 || data[19] != 0 {
		t.Fatal("bounded Linux amd64 ELF artifact required")
	}
	client := livePrivilegeClient(t)
	machine := liveMachine(t, client, vmid)
	if machine.Status != "running" || machine.Template || !isManagedDesktop(machine) {
		t.Fatal("running managed acceptance desktop required")
	}
	exec := func(args ...string) string {
		t.Helper()
		result, err := liveExecGuest(t, client, machine, args)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("Guest installation: exit=%d stderr=%s transport=%v", result.ExitCode, result.Stderr, err)
		}
		return strings.TrimSpace(result.Stdout)
	}
	if exec("uname", "-m") != "x86_64" {
		t.Fatal("architecture mismatch")
	}
	const destination = "/usr/local/sbin/vc-workspace-guest-agent"
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	current := exec("/bin/sh", "-c", "test ! -L "+destination+" && if test -e "+destination+"; then sha256sum "+destination+"; fi")
	previousHash := ""
	if current != "" {
		if strings.HasPrefix(current, hash+" ") {
			if !agentSessionInstallCapabilities(exec(destination, "computer-v2-capabilities")) {
				t.Fatal("identical artifact lacks the required fenced session protocol")
			}
			t.Log("identical acceptance artifact already installed")
			return
		}
		previousHash = os.Getenv("VC_WORKSPACE_LIVE_AGENT_SESSION_REPLACE_SHA256")
		if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(previousHash) || !strings.HasPrefix(current, previousHash+" ") {
			t.Fatal("different installed artifact exists; explicit matching REPLACE_SHA256 required")
		}
	}
	stage := exec("/bin/sh", "-c", "test ! -L /usr/local/sbin && test \"$(stat -c '%u' /usr/local/sbin)\" = 0 && mktemp -d /usr/local/sbin/.vcw-agent-session.XXXXXXXXXX")
	if !regexp.MustCompile(`^/usr/local/sbin/\.vcw-agent-session\.[a-zA-Z0-9]{10}$`).MatchString(stage) {
		t.Fatal("unexpected staging directory")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := client.ExecGuest(ctx, machine.Node, vmid, []string{"/bin/sh", "-c", "rm -f -- " + stage + "/part " + stage + "/agent && rmdir -- " + stage})
		if err != nil || result.ExitCode != 0 {
			t.Error("private artifact staging cleanup failed", err)
		}
	})
	for offset := 0; offset < len(data); offset += 45 * 1024 {
		end := min(offset+45*1024, len(data))
		if err := client.WriteGuestBinaryFile(t.Context(), machine.Node, vmid, stage+"/part", data[offset:end]); err != nil {
			t.Fatal("artifact chunk upload failed", err)
		}
		exec("/bin/sh", "-c", "cat "+stage+"/part >>"+stage+"/agent")
		if offset%(45*1024*32) == 0 {
			t.Logf("staged %d/%d artifact bytes", end, len(data))
		}
	}
	if !strings.HasPrefix(exec("sha256sum", stage+"/agent"), hash+" ") {
		t.Fatal("uploaded artifact hash mismatch")
	}
	exec("chmod", "0755", stage+"/agent")
	if !agentSessionInstallCapabilities(exec(stage+"/agent", "computer-v2-capabilities")) {
		t.Fatal("artifact lacks the required fenced session protocol; not installed")
	}
	exec(stage+"/agent", "computer-v2-control-init")
	if previousHash != "" {
		backup := destination + ".previous-" + previousHash[:12]
		exec("/bin/sh", "-c", "set -eu\nif test -e "+backup+"; then test \"$(sha256sum "+backup+" | cut -d ' ' -f 1)\" = "+previousHash+"; else ln "+destination+" "+backup+"; fi\ntest \"$(sha256sum "+destination+" | cut -d ' ' -f 1)\" = "+previousHash+"\nmv -fT "+stage+"/agent "+destination)
		t.Logf("previous acceptance artifact retained at %s", backup)
	} else {
		exec("ln", stage+"/agent", destination)
	}
	t.Logf("VM %d: installed %d bytes, SHA256 %s; legacy service and existing sessions untouched", vmid, len(data), hash)
}

func agentSessionInstallCapabilities(raw string) bool {
	var capability struct {
		SchemaVersion      int    `json:"schema_version"`
		SessionTransport   string `json:"session_transport"`
		AuthorityTransport string `json:"authority_transport"`
		AccountFence       string `json:"experimental_account_fence"`
	}
	return len(raw) <= 4096 && json.Unmarshal([]byte(raw), &capability) == nil &&
		capability.SchemaVersion == computer.SessionSchemaVersion && capability.SessionTransport == "unix_peercred" && capability.AuthorityTransport == "stdin_epoch_account_v2" && capability.AccountFence == "lease_epoch_login_generation_v1"
}

func TestAgentSessionInstallRejectsStaleProtocol(t *testing.T) {
	good := `{"schema_version":2,"session_transport":"unix_peercred","authority_transport":"stdin_epoch_account_v2","experimental_account_fence":"lease_epoch_login_generation_v1"}`
	if !agentSessionInstallCapabilities(good) {
		t.Fatal("current protocol rejected")
	}
	for _, bad := range []string{"", `{"schema_version":2,"session_transport":"unix_peercred"}`, strings.ReplaceAll(good, "lease_epoch_login_generation_v1", "lease_epoch_v1"), strings.ReplaceAll(good, "stdin_epoch_account_v2", "stdin_epoch_v1"), strings.ReplaceAll(good, "unix_peercred", "named_pipe_sid"), strings.Repeat(" ", 4097) + good, good + "{}"} {
		if agentSessionInstallCapabilities(bad) {
			t.Fatal("stale or invalid artifact accepted")
		}
	}
}

func TestLiveAgentSessionPreflight(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_AGENT_SESSION_PREFLIGHT") != "true" {
		t.Skip("opt-in read-only Agent session preflight")
	}
	vmid, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_AGENT_SESSION_VMID"))
	if err != nil || vmid <= 0 {
		t.Fatal("explicit positive acceptance VMID required")
	}
	client := livePrivilegeClient(t)
	machine := liveMachine(t, client, vmid)
	if machine.Status != "running" || machine.Template || !isManagedDesktop(machine) {
		t.Fatal("running managed acceptance desktop required")
	}
	result, err := liveExecGuest(t, client, machine, []string{"/bin/sh", "-c", `set -eu
uname -m
dpkg-query -W -f='${Package} ${Version}\n' xrdp xorgxrdp
command -v xrdp-sesrun || true
command -v cargo || true
test ! -e /usr/local/sbin/vc-workspace-guest-agent || sha256sum /usr/local/sbin/vc-workspace-guest-agent
for item in /var/lib/vc-vdi /var/lib/vc-vdi/computer /var/lib/vc-workspace /var/lib/vc-workspace/computer; do
  test ! -e "$item" || stat -c '%n %u:%g %a' "$item"
done
test ! -e /usr/local/sbin/vc-vdi-guest-agent || sha256sum /usr/local/sbin/vc-vdi-guest-agent
dpkg-query -W -f='${Package} ${Version}\n' python3-gi gir1.2-gtk-3.0 at-spi2-core || true
awk '/^(KillDisconnected|DisconnectedTimeLimit|IdleTimeLimit|Policy|MaxSessions|EnableUserWindowManager|UserWindowManager|DefaultWindowManager)=/' /etc/xrdp/sesman.ini
systemctl is-active xrdp xrdp-sesman qemu-guest-agent
ps -C Xorg -o uid,pid,ppid,args || true
command -v xrdp-sesadmin || true
python3 -c '
import json,os,stat
for path in ["/var/lib/vc-workspace/computer-v2/authority.json","/var/lib/vc-workspace/computer-v2/authority-fenced.json","/var/lib/vc-workspace/computer-v2/authority-account-fenced.json"]:
 try: f=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
 except FileNotFoundError: print(path,"absent"); continue
 with os.fdopen(f,"rb") as stream:
  st=os.fstat(stream.fileno()); assert stat.S_ISREG(st.st_mode) and st.st_uid==0 and not st.st_mode&0o022 and st.st_size<=65536
  a=json.load(stream)["authority"]
  print(path,json.dumps({k:a[k] for k in ["control_epoch","state","expires_unix_ms"]}))
'
`})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("preflight failed: %v exit=%d", err, result.ExitCode)
	}
	t.Logf("VM %d on %s: %s", vmid, machine.Node, result.Stdout)
}

// Owns two random Agent accounts in an isolated database schema. Existing OS
// users and xrdp configuration are never changed by this acceptance fixture.
func TestLiveAgentSessionLifecycle(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_AGENT_SESSION") != "true" {
		t.Skip("opt-in disposable Agent OS session acceptance")
	}
	vmid, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_AGENT_SESSION_VMID"))
	if err != nil || vmid <= 0 {
		t.Fatal("explicit positive acceptance VMID required")
	}
	client := livePrivilegeClient(t)
	machine := liveMachine(t, client, vmid)
	if machine.Status != "running" || machine.Template || !isManagedDesktop(machine) {
		t.Fatal("running managed acceptance desktop required")
	}
	exec := func(script string) string {
		t.Helper()
		result, err := liveExecGuest(t, client, machine, []string{"/bin/sh", "-c", script})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("Guest assertion: exit=%d out=%s stderr=%s transport=%v", result.ExitCode, result.Stdout, result.Stderr, err)
		}
		return strings.TrimSpace(result.Stdout)
	}
	exec("test -x /usr/local/sbin/vc-workspace-guest-agent && command -v xfce4-terminal && command -v xrdp-sesrun")
	configurationBefore := exec("sha256sum /etc/xrdp/xrdp.ini /etc/xrdp/sesman.ini")
	// Import only settings verified from the acceptance VM, so registering a
	// fresh DB does not rewrite global policy or restart somebody else's RDP.
	raw := exec(`python3 -c '
import configparser,json,pathlib
x=configparser.ConfigParser(strict=False,interpolation=None); x.read("/etc/xrdp/xrdp.ini")
s=configparser.ConfigParser(strict=False,interpolation=None); s.read("/etc/xrdp/sesman.ini")
c=x.getboolean("Channels","cliprdr"); d=x.getboolean("Channels","rdpdr")
assert s.get("Security","RestrictInboundClipboard")==("none" if c else "all")
assert s.get("Security","RestrictOutboundClipboard")==("none" if c else "all")
assert s.getboolean("Chansrv","EnableFuseMount")==d
print(json.dumps({"clipboard":c,"drive":d,"background":pathlib.Path("/etc/vc-workspace/background-policy").read_text().strip()=="managed"}))
'`)
	var actual struct {
		Clipboard  bool
		Drive      bool
		Background bool
	}
	if err := json.Unmarshal([]byte(raw), &actual); err != nil {
		t.Fatal(err)
	}
	db, suffix := regressionDB(t)
	if err := db.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: vmid, Node: machine.Node, DisplayName: machine.Name, OSFamily: "linux"}); err != nil {
		t.Fatal(err)
	}
	policy, err := db.PutDesktopAccessPolicy(t.Context(), vmid, "standard", actual.Clipboard, actual.Drive, actual.Background, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkDesktopAccessPolicyApplied(t.Context(), vmid, policy.DesiredRevision, "linux"); err != nil {
		t.Fatal(err)
	}
	executor := computer.NewPVEExecutor(observedAgentSessionGuest{client, t})
	s := New(Dependencies{Store: db, PVE: client, Computer: executor, InternalAPIToken: "test-" + suffix})
	server := httptest.NewServer(s.Handler())
	t.Cleanup(server.Close)
	api, err := agentapi.New(server.URL, "test-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if _, err := db.RevokeDesktopLeases(ctx, vmid); err != nil {
			t.Error("close disposable leases", err)
			return
		}
		for _, name := range names {
			if err := testguest.StopAgentAccount(ctx, db, executor, machine, name); err != nil {
				t.Error("stop disposable Agent session", err)
				continue
			}
			// Name collision was refused before registering cleanup. No globs or
			// existing user Home is in the deletion scope.
			script := "set -eu\nif getent -s files passwd " + name + " >/dev/null; then userdel -r " + name + "; fi\nrm -f -- /etc/sudoers.d/vc-workspace-" + name + " /etc/sudoers.d/vc-workspace-" + name + ".tmp\nrm -rf -- /var/lib/vc-workspace/computer-v2/users/" + name + "\n! getent -s files passwd " + name
			result, err := client.ExecGuest(ctx, machine.Node, vmid, []string{"/bin/sh", "-c", script})
			if err != nil || result.ExitCode != 0 {
				t.Error("remove disposable Agent account", err, result.ExitCode)
			}
		}
		result, err := client.ExecGuest(ctx, machine.Node, vmid, []string{"sha256sum", "/etc/xrdp/xrdp.ini", "/etc/xrdp/sesman.ini"})
		if err != nil || strings.TrimSpace(result.Stdout) != configurationBefore {
			t.Error("existing xrdp settings changed", err)
		}
	})
	previousTargets := map[string]computer.SessionTarget{}
	previousMarkers := map[string]string{}
	for index, event := range []string{"release", "assignment_removed", "expiry_after_reacquire"} {
		agentID := fmt.Sprintf("session-live-%s-%d", suffix, index)
		if index == 2 {
			agentID = fmt.Sprintf("session-live-%s-0", suffix)
		}
		name := store.AgentGuestUsername(agentID)
		if !slices.Contains(names, name) {
			exec("! getent passwd " + name + " && test ! -e /home/" + name + " && test ! -e /var/lib/vc-workspace/computer-v2/users/" + name)
			names = append(names, name)
			if _, err := db.CreateAgentPrincipal(t.Context(), store.AgentPrincipal{ID: agentID, DisplayName: "Disposable Agent session"}, auth.TokenDigest("agent-"+agentID)); err != nil {
				t.Fatal(err)
			}
		} else if exec("cat /home/"+name+"/acceptance-input") != previousMarkers[name] {
			t.Fatal("revocation lost the Agent persistent Home")
		}
		if _, err := db.PutDesktopAssignment(t.Context(), store.DesktopAssignment{SubjectType: "agent", SubjectID: agentID, DesktopVMID: vmid}); err != nil {
			t.Fatal(err)
		}
		ttl := 300
		if index == 2 {
			ttl = 60
		}
		lease, err := api.AcquireDesktop(t.Context(), agentID, vmid, ttl)
		if err != nil {
			t.Fatal("acquire", err)
		}
		started := time.Now()
		shot, err := api.ComputerAction(t.Context(), agentID, lease.ID, agentapi.ComputerAction{ControlEpoch: lease.ControlEpoch, Operation: computer.OperationScreenshot, Screenshot: &computer.Screenshot{MaxWidth: 1280}})
		if err != nil {
			t.Log(exec("test ! -f /home/" + name + "/.xsession-errors || tail -n 40 /home/" + name + "/.xsession-errors"))
			t.Fatal("first unattended screenshot", err)
		}
		if shot.Screenshot == nil || !shot.OK {
			t.Fatal("missing desktop screenshot")
		}
		data, err := base64.StdEncoding.DecodeString(shot.Screenshot.Data)
		if err != nil || len(data) < 1000 || fmt.Sprintf("%x", sha256.Sum256(data)) != shot.Screenshot.SHA256 {
			t.Fatal("invalid desktop image", err)
		}
		target, err := executor.DiscoverSession(t.Context(), machine, name)
		if err != nil || target.Username != name {
			t.Fatal("incorrect Agent identity", err)
		}
		if old, exists := previousTargets[name]; exists && (old.UID != target.UID || old.InstanceID == target.InstanceID) {
			t.Fatal("reacquired Agent must retain UID but use a fresh Helper instance")
		}
		previousTargets[name] = target
		t.Logf("VM %d: %s bootstrapped uid=%d session=%s; %dx%d screenshot in %s", vmid, name, target.UID, target.SessionID, shot.Screenshot.Width, shot.Screenshot.Height, time.Since(started).Round(time.Millisecond))
		tree, err := api.ComputerAction(t.Context(), agentID, lease.ID, agentapi.ComputerAction{ControlEpoch: lease.ControlEpoch, Operation: computer.OperationAccessibility, Accessibility: &computer.Accessibility{MaxDepth: 6, MaxNodes: 100}})
		if err != nil || tree.Accessibility == nil || tree.Accessibility.Source != "linux_atspi" || len(tree.Accessibility.Nodes) == 0 {
			t.Fatal("real interactive AT-SPI unavailable", err)
		}
		display := ":" + target.SessionID[strings.LastIndex(target.SessionID, ":")+1:]
		// Only this disposable desktop receives an input fixture. No process
		// environment or screen belonging to existing users is inspected.
		exec(fmt.Sprintf("runuser -u %s -- env DISPLAY=%s XAUTHORITY=/home/%s/.Xauthority /bin/sh -c 'nohup xfce4-terminal --disable-server --title=VCWorkspaceAcceptance >/home/%s/fixture.log 2>&1 </dev/null &'", name, display, name, name))
		// Wait for the private fixture, not for a fixed UI animation duration.
		deadline := time.Now().Add(10 * time.Second)
		for {
			result, err := client.ExecGuest(t.Context(), machine.Node, vmid, []string{"pgrep", "-u", name, "-x", "xfce4-terminal"})
			if err == nil && result.ExitCode == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("private terminal fixture missing")
			}
			time.Sleep(100 * time.Millisecond)
		}
		marker := fmt.Sprintf("vcw-input-%s-%d", suffix, index)
		_, err = api.ComputerAction(t.Context(), agentID, lease.ID, agentapi.ComputerAction{ControlEpoch: lease.ControlEpoch, Operation: computer.OperationTypeText, Text: &computer.Text{Value: "printf %s " + marker + " > /home/" + name + "/acceptance-input", Sensitive: true}})
		if err != nil {
			t.Fatal("private terminal input", err)
		}
		_, err = api.ComputerAction(t.Context(), agentID, lease.ID, agentapi.ComputerAction{ControlEpoch: lease.ControlEpoch, Operation: computer.OperationKey, Key: &computer.Key{Key: "enter"}})
		if err != nil {
			t.Fatal("terminal Enter", err)
		}
		if exec("cat /home/"+name+"/acceptance-input") != marker {
			t.Fatal("input did not reach the target OS session")
		}
		previousMarkers[name] = marker
		if event == "release" {
			if _, err := api.ReleaseDesktop(t.Context(), agentID, lease.ID); err != nil {
				t.Fatal("release", err)
			}
		} else if event == "assignment_removed" {
			if _, err := db.DeleteDesktopAssignment(t.Context(), "agent", agentID, vmid); err != nil {
				t.Fatal(err)
			}
			s.revokeDueComputerAuthorities(t.Context())
		} else {
			if delay := time.Until(lease.ExpiresAt) + 10*time.Millisecond; delay > 0 {
				time.Sleep(delay)
			}
			// Host and isolated PostgreSQL clocks need not agree to a millisecond.
			// Exercise the maintenance loop until its normal eventual convergence.
			deadline := time.Now().Add(10 * time.Second)
			for {
				if err := db.QueueExpiredComputerLeases(t.Context(), 0); err != nil {
					t.Fatal("expiration scan", err)
				}
				s.revokeDueComputerAuthorities(t.Context())
				bindings, err := db.AgentGuestSessions(t.Context(), vmid)
				if err != nil {
					t.Fatal(err)
				}
				finished := slices.ContainsFunc(bindings, func(item store.AgentGuestSession) bool { return item.AgentID == agentID && item.State == "disabled" })
				if finished {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("expired Agent session did not converge")
				}
				time.Sleep(250 * time.Millisecond)
			}
		}
		pending, err := db.PendingComputerRevocations(t.Context(), vmid)
		if err != nil || len(pending) != 0 {
			t.Fatal("OS revocation still pending", err)
		}
		exec("! pgrep -u " + name + " && getent shadow " + name + " | awk -F: '$2 ~ /^!/ && $8 == 1 {found=1} END {exit !found}'")
		if _, err := api.ComputerAction(t.Context(), agentID, lease.ID, agentapi.ComputerAction{ControlEpoch: lease.ControlEpoch, Operation: computer.OperationScreenshot}); err == nil {
			t.Fatal("revoked caller retained computer access")
		}
		t.Logf("%s: real screenshot/AT-SPI/input passed; account locked, all user processes stopped, stale action rejected", event)
	}
}
