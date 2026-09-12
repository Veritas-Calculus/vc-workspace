package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/agentapi"
	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/httpapi"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
	"github.com/Veritas-Calculus/vc-workspace/internal/testguest"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Diagnostics are confined to fixed V2 lifecycle commands, never actions,
// stdin, screenshots or arbitrary Guest output.
type liveMCPGuest struct {
	*pve.Client
	t *testing.T
}

func (g liveMCPGuest) ExecGuest(ctx context.Context, node string, vmid int, command []string) (pve.GuestExecResult, error) {
	result, err := g.Client.ExecGuest(ctx, node, vmid, command)
	g.diagnostic(command, result, err)
	return result, err
}

func (g liveMCPGuest) ExecGuestWithInput(ctx context.Context, node string, vmid int, command []string, input []byte) (pve.GuestExecResult, error) {
	result, err := g.Client.ExecGuestWithInput(ctx, node, vmid, command, input)
	g.diagnostic(command, result, err)
	return result, err
}

func (g liveMCPGuest) diagnostic(command []string, result pve.GuestExecResult, err error) {
	if len(command) > 1 && command[0] == "/usr/local/sbin/vc-workspace-guest-agent" && slices.Contains([]string{"computer-v2-authority", "computer-v2-account-provision", "computer-v2-account-login", "computer-v2-account-lease", "computer-v2-account-inspect", "computer-v2-init", "computer-v2-capabilities"}, command[1]) && (err != nil || result.ExitCode != 0) {
		g.t.Logf("fixed Guest lifecycle %s failed: exit=%d stderr=%s transport=%v", command[1], result.ExitCode, result.Stderr, err)
	}
}

// Requires an explicitly selected Linux acceptance VM with the current Guest
// binary. Owns two random local OS accounts and an isolated PostgreSQL schema.
// MCP initialization and every tool call use the SDK over real TCP HTTP. QGA is
// used only for fixture setup and independent OS assertions, never as tool input.
func TestLiveMCPComputerUse(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_MCP_COMPUTER_AUDIT") != "true" {
		t.Skip("opt-in full MCP to PVE/Guest acceptance")
	}
	vmid, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_COMPUTER_VMID"))
	if err != nil || vmid <= 0 || os.Getenv("VC_WORKSPACE_TEST_DATABASE_URL") == "" {
		t.Fatal("explicit VMID and VC_WORKSPACE_TEST_DATABASE_URL required")
	}
	client := liveMCPPVEClient(t)
	machine := liveMCPMachine(t, client, vmid)
	exec := func(script string) string {
		t.Helper()
		result, err := client.ExecGuest(t.Context(), machine.Node, vmid, []string{"/bin/sh", "-c", script})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("private Guest fixture/assertion failed: exit=%d transport=%v stderr=%s", result.ExitCode, err, result.Stderr)
		}
		return strings.TrimSpace(result.Stdout)
	}
	exec("test -x /usr/local/sbin/vc-workspace-guest-agent && command -v xfce4-terminal && command -v xrdp-sesrun")
	configurationBefore := exec("sha256sum /etc/xrdp/xrdp.ini /etc/xrdp/sesman.ini")
	existingXorg := exec("ps -C Xorg -o uid=,pid= || true")
	// Verify/import existing global settings instead of restarting anybody's RDP
	// connection when the disposable control-plane database is first populated.
	actualPolicy := exec(`python3 -c '
import configparser,json,pathlib
x=configparser.ConfigParser(strict=False,interpolation=None); x.read("/etc/xrdp/xrdp.ini")
s=configparser.ConfigParser(strict=False,interpolation=None); s.read("/etc/xrdp/sesman.ini")
c=x.getboolean("Channels","cliprdr"); d=x.getboolean("Channels","rdpdr")
assert s.get("Security","RestrictInboundClipboard")==("none" if c else "all")
assert s.get("Security","RestrictOutboundClipboard")==("none" if c else "all")
assert s.getboolean("Chansrv","EnableFuseMount")==d
print(json.dumps({"clipboard":c,"drive":d,"background":pathlib.Path("/etc/vc-workspace/background-policy").read_text().strip()=="managed"}))
'`)
	var actual struct{ Clipboard, Drive, Background bool }
	if err := json.Unmarshal([]byte(actualPolicy), &actual); err != nil {
		t.Fatal(err)
	}
	databaseURL := testdb.URL(t)
	db, err := store.Open(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: vmid, Node: machine.Node, DisplayName: machine.Name, OSFamily: "linux", Present: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	policy, err := db.PutDesktopAccessPolicy(t.Context(), vmid, "standard", actual.Clipboard, actual.Drive, actual.Background, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkDesktopAccessPolicyApplied(t.Context(), vmid, policy.DesiredRevision, "linux"); err != nil {
		t.Fatal(err)
	}
	suffix, err := auth.OpaqueToken(12)
	if err != nil {
		t.Fatal(err)
	}
	executor := computer.NewPVEExecutor(liveMCPGuest{client, t})
	// Only this explicitly opted-in acceptance fixture imports a manually
	// confirmed inactive Guest fence into its NEW private schema. Production
	// must never silently adopt a Guest epoch after database loss or rollback.
	floor, err := strconv.ParseInt(os.Getenv("VC_WORKSPACE_LIVE_COMPUTER_EPOCH_FLOOR"), 10, 64)
	if err != nil || floor < 0 || floor > 1<<62 {
		t.Fatal("explicit inactive Guest EPOCH_FLOOR required; run read-only preflight first")
	}
	observed := exec(`python3 -c '
import json,os,stat,time
base="/var/lib/vc-workspace/computer-v2"
for path in ["/var","/var/lib","/var/lib/vc-workspace",base]:
 s=os.lstat(path); assert stat.S_ISDIR(s.st_mode) and s.st_uid==0 and not s.st_mode&0o022
floor=0
for name in ["authority.json","authority-fenced.json","authority-account-fenced.json"]:
 try: f=os.open(base+"/"+name,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
 except FileNotFoundError: continue
 with os.fdopen(f,"rb") as stream:
  s=os.fstat(stream.fileno()); assert stat.S_ISREG(s.st_mode) and s.st_uid==0 and s.st_nlink==1 and not s.st_mode&0o022 and 0<s.st_size<=65536
  b=json.load(stream); a=b["authority"]
  assert b["schema_version"]==2 and a["schema_version"]==1 and a["state"]=="revoked" and b.get("target") is None
  assert type(a["control_epoch"]) is int and 0<a["control_epoch"]<=2**62 and a["expires_unix_ms"]<=time.time()*1000
  floor=max(floor,a["control_epoch"])
print(floor)
'`)
	if observed != strconv.FormatInt(floor, 10) {
		t.Fatal("Guest epoch changed since preflight; refusing fixture initialization")
	}
	private, err := pgx.Connect(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = private.Exec(t.Context(), `INSERT INTO desktop_computer_epochs(desktop_vmid,control_epoch) VALUES($1,$2)`, vmid, floor+1)
	_ = private.Close(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.RevokeSessionAuthority(t.Context(), machine, computer.Authority{SchemaVersion: computer.SchemaVersion, LeaseID: "lease_acceptance_initial_fence", ControlEpoch: floor + 1, State: "revoked"}); err != nil {
		t.Fatal("initialize acceptance fence without deleting prior history", err)
	}
	var owned []string
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		// Close only leases in this test's disposable schema, even on a failed
		// assertion. Leave a persistent revoked fence; never delete/reset it.
		if _, err := db.RevokeDesktopLeases(ctx, vmid); err != nil {
			t.Error("close fixture leases", err)
		}
		epoch, err := db.ComputerControlEpoch(ctx, vmid)
		if err != nil {
			t.Error("read fixture closing epoch", err)
		} else if err := executor.RevokeSessionAuthority(ctx, machine, computer.Authority{SchemaVersion: computer.SchemaVersion, LeaseID: "lease_acceptance_final_fence", ControlEpoch: epoch, State: "revoked"}); err != nil {
			t.Error("fence fixture cleanup", err)
		}
		for _, name := range owned {
			if err := testguest.StopAgentAccount(ctx, db, executor, machine, name); err != nil {
				t.Error("stop disposable Agent session", err)
				continue
			}
			// Each exact target was proved absent before creation; never touch an
			// existing user's Home or remove the shared computer-v2 directory.
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
		result, err = client.ExecGuest(ctx, machine.Node, vmid, []string{"/bin/sh", "-c", "ps -C Xorg -o uid=,pid= || true"})
		if err != nil || strings.TrimSpace(result.Stdout) != existingXorg {
			t.Error("existing Xorg sessions changed or fixture process remains", err)
		}
	})
	agentIDs := []string{"mcp-live-a-" + suffix, "mcp-live-b-" + suffix}
	tokens := []string{"vcwa_a_" + suffix, "vcwa_b_" + suffix}
	for index, agentID := range agentIDs {
		name := store.AgentGuestUsername(agentID)
		exec("! getent passwd " + name + " && test ! -e /home/" + name + " && test ! -e /var/lib/vc-workspace/computer-v2/users/" + name)
		owned = append(owned, name)
		if _, err := db.CreateAgentPrincipal(t.Context(), store.AgentPrincipal{ID: agentID, DisplayName: "Disposable MCP Agent"}, auth.TokenDigest(tokens[index])); err != nil {
			t.Fatal(err)
		}
	}
	assign := func(agentID string) {
		t.Helper()
		if _, err := db.PutDesktopAssignment(t.Context(), store.DesktopAssignment{SubjectType: "agent", SubjectID: agentID, DesktopVMID: vmid}); err != nil {
			t.Fatal(err)
		}
	}
	assign(agentIDs[0])
	internalToken := "internal-" + suffix
	s := httpapi.New(httpapi.Dependencies{PVE: client, Store: db, Computer: executor, InternalAPIToken: internalToken, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	controlPlane := httptest.NewServer(s.Handler())
	t.Cleanup(controlPlane.Close)
	maintenance, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.RunBackgroundMaintenance(maintenance) }()
	t.Cleanup(func() { stop(); <-done })
	endpoint := testMCPHTTPServer(t, controlPlane.URL, internalToken)
	agentA := testMCPClient(t, endpoint.URL+"/mcp", tokens[0])
	agentB := testMCPClient(t, endpoint.URL+"/mcp", tokens[1])
	catalogue, err := agentA.ListTools(t.Context(), nil)
	if err != nil || len(catalogue.Tools) != 10 {
		t.Fatal("MCP initialization/catalogue", err)
	}
	var desktops agentapi.DesktopList
	liveMCPCall(t, agentA, "desktop_list", noInput{}, &desktops)
	if len(desktops.Desktops) != 1 || desktops.Desktops[0].VMID != vmid {
		t.Fatal("assigned desktop missing")
	}
	liveMCPCall(t, agentB, "desktop_list", noInput{}, &desktops)
	if len(desktops.Desktops) != 0 {
		t.Fatal("unassigned Agent could list the desktop")
	}
	liveMCPDenied(t, agentB, "desktop_acquire", map[string]any{"desktop_vmid": vmid, "ttl_seconds": 300})
	var lease agentapi.Lease
	liveMCPCall(t, agentA, "desktop_acquire", acquireInput{DesktopVMID: vmid, TTLSeconds: 300}, &lease)
	if lease.AgentID != agentIDs[0] || lease.ControlEpoch <= floor+1 {
		t.Fatal("lease identity or epoch incorrect")
	}
	liveMCPDenied(t, agentB, "desktop_lease_get", leaseInput{LeaseID: lease.ID})
	liveMCPDenied(t, agentB, "desktop_screenshot", screenshotInput{LeaseID: lease.ID, ControlEpoch: lease.ControlEpoch})
	// The schema must reject, not honor, an identity smuggled through input.
	liveMCPDenied(t, agentB, "desktop_screenshot", map[string]any{"lease_id": lease.ID, "control_epoch": lease.ControlEpoch, "agent_id": agentIDs[0]})
	shot := func(session *mcp.ClientSession, current agentapi.Lease) screenshotOutput {
		t.Helper()
		return checkMCPScreenshot(t, liveMCPCall(t, session, "desktop_screenshot", screenshotInput{LeaseID: current.ID, ControlEpoch: current.ControlEpoch, MaxWidth: 640, TimeoutMS: 15000}, nil))
	}
	started := time.Now()
	metadata := shot(agentA, lease)
	if metadata.Screenshot.Width != 640 || metadata.Screenshot.DesktopBounds.Width != 1600 {
		t.Fatal("downscaled image did not retain native 1600-pixel coordinate space")
	}
	target, err := executor.DiscoverSession(t.Context(), machine, owned[0])
	if err != nil || target.Username != owned[0] {
		t.Fatal("MCP bootstrapped the wrong OS identity", err)
	}
	checkBinding := func(expected computer.SessionTarget, state string) store.AgentGuestSession {
		t.Helper()
		bindings, err := db.AgentGuestSessions(t.Context(), vmid)
		if err != nil {
			t.Fatal(err)
		}
		for _, binding := range bindings {
			if binding.GuestUsername != expected.Username {
				continue
			}
			if binding.OSFamily != "linux" || binding.GuestUID != expected.UID || binding.GuestSID != "" || binding.State != state {
				t.Fatal("durable Agent account does not match actual Guest identity")
			}
			if state == "disabled" && (binding.SessionID != "" || binding.InstanceID != "") {
				t.Fatal("disabled account retained its live session binding")
			}
			if state == "ready" && (binding.SessionID != expected.SessionID || binding.InstanceID != expected.InstanceID) {
				t.Fatal("persisted instance does not match authenticated Helper")
			}
			if binding.LoginGeneration < 1 {
				t.Fatal("real login lacks a reserved database generation")
			}
			intent := computer.AccountLease{SchemaVersion: 1, Identity: computer.AccountIdentity{Username: binding.GuestUsername, UID: binding.GuestUID, SID: binding.GuestSID}, LeaseID: binding.LeaseID, ControlEpoch: binding.ControlEpoch, LoginGeneration: binding.LoginGeneration}
			phase := "revoked"
			if state == "ready" {
				phase, intent.ExpiresUnixSeconds = "sealed", binding.ExpiresAt.Unix()
			}
			observed, err := executor.InspectAgentAccount(t.Context(), machine, binding.GuestUsername)
			if err != nil || !observed.Matches(intent, phase) || (state == "disabled" && !observed.LoginStopped()) || (state == "ready" && (observed.Disabled || observed.ProcessesAbsent == nil || *observed.ProcessesAbsent)) {
				t.Fatal("independent Guest observation does not match database login generation/lifecycle", err)
			}
			return binding
		}
		t.Fatal("durable Agent account was not recorded")
		return store.AgentGuestSession{}
	}
	checkBinding(target, "ready")
	t.Logf("SDK initialize/list/acquire and unattended image passed: VM %d uid=%d image=%dx%d desktop=%dx%d in %s", vmid, target.UID, metadata.Screenshot.Width, metadata.Screenshot.Height, metadata.Screenshot.DesktopBounds.Width, metadata.Screenshot.DesktopBounds.Height, time.Since(started).Round(time.Millisecond))
	display := ":" + target.SessionID[strings.LastIndex(target.SessionID, ":")+1:]
	exec(fmt.Sprintf("runuser -u %s -- env DISPLAY=%s XAUTHORITY=/home/%s/.Xauthority /bin/sh -c 'nohup xfce4-terminal --disable-server --title=VCWorkspaceMCPAcceptance >/home/%s/fixture.log 2>&1 </dev/null &'", owned[0], display, owned[0], owned[0]))
	var tree computer.Response
	var terminal computer.AccessibilityNode
	deadline := time.Now().Add(15 * time.Second)
	for {
		liveMCPCall(t, agentA, "desktop_accessibility_snapshot", accessibilityInput{LeaseID: lease.ID, ControlEpoch: lease.ControlEpoch, MaxNodes: 100, MaxDepth: 6, TimeoutMS: 15000}, &tree)
		if tree.Accessibility != nil && tree.Accessibility.Source == "linux_atspi" {
			for _, node := range tree.Accessibility.Nodes {
				if strings.Contains(node.Name, "VCWorkspaceMCPAcceptance") && node.Width > 100 && node.Height > 100 {
					terminal = node
					break
				}
			}
		}
		if terminal.Width > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("private terminal accessibility node missing")
		}
		time.Sleep(150 * time.Millisecond)
	}
	// Locate a known private target semantically, then exercise the screenshot
	// pixel -> desktop coordinate conversion before clicking and typing.
	metadata = shot(agentA, lease)
	bounds := metadata.Screenshot.DesktopBounds
	u := (terminal.X + terminal.Width/2 - bounds.X) * metadata.Screenshot.Width / bounds.Width
	v := (terminal.Y + terminal.Height/2 - bounds.Y) * metadata.Screenshot.Height / bounds.Height
	liveMCPCall(t, agentA, "desktop_mouse", mouseInput{LeaseID: lease.ID, ControlEpoch: lease.ControlEpoch, Action: "click", X: bounds.X + u*bounds.Width/metadata.Screenshot.Width, Y: bounds.Y + v*bounds.Height/metadata.Screenshot.Height, Button: "left"}, nil)
	marker := "mcp-private-" + suffix
	liveMCPCall(t, agentA, "desktop_type_text", typeTextInput{LeaseID: lease.ID, ControlEpoch: lease.ControlEpoch, Text: "printf %s " + marker + " > /home/" + owned[0] + "/acceptance-input", Sensitive: true}, nil)
	liveMCPCall(t, agentA, "desktop_key", keyInput{LeaseID: lease.ID, ControlEpoch: lease.ControlEpoch, Key: "enter"}, nil)
	if exec("cat /home/"+owned[0]+"/acceptance-input") != marker {
		t.Fatal("MCP input did not reach the Agent private desktop")
	}
	// Simulate the loss of only this fixture's private OS session. A new login
	// must reserve a new generation without changing the lease or its deadline.
	beforeLogin := checkBinding(target, "ready")
	if beforeLogin.LoginGeneration != 1 {
		t.Fatal("healthy repeated MCP actions unexpectedly opened another login")
	}
	exec(fmt.Sprintf("set -eu\ntest \"$(getent -s files passwd %s | cut -d: -f3)\" = %d\nstatus=0; pkill -KILL -u %d || status=$?; test \"$status\" -le 1\nn=0; while pgrep -u %d >/dev/null; do n=$((n+1)); test \"$n\" -lt 100; sleep 0.1; done", owned[0], target.UID, target.UID, target.UID))
	shot(agentA, lease)
	reconnected, err := executor.DiscoverSession(t.Context(), machine, owned[0])
	if err != nil || reconnected.UID != target.UID || reconnected.InstanceID == target.InstanceID {
		t.Fatal("same-Lease login did not create a new Helper for the original UID", err)
	}
	afterLogin := checkBinding(reconnected, "ready")
	if afterLogin.LoginGeneration != beforeLogin.LoginGeneration+1 || afterLogin.LeaseID != beforeLogin.LeaseID || afterLogin.ControlEpoch != beforeLogin.ControlEpoch || afterLogin.Generation != beforeLogin.Generation || !afterLogin.ExpiresAt.Equal(beforeLogin.ExpiresAt) {
		t.Fatal("same-Lease reconnect changed ownership/deadline or skipped login generation")
	}
	staleIntent := computer.AccountLease{SchemaVersion: 1, Identity: computer.AccountIdentity{Username: beforeLogin.GuestUsername, UID: beforeLogin.GuestUID, SID: beforeLogin.GuestSID}, LeaseID: beforeLogin.LeaseID, ControlEpoch: beforeLogin.ControlEpoch, LoginGeneration: beforeLogin.LoginGeneration, ExpiresUnixSeconds: beforeLogin.ExpiresAt.Unix()}
	if _, err := executor.StartAgentSession(t.Context(), machine, staleIntent); err == nil {
		t.Fatal("Guest accepted a late old-generation login")
	}
	if err := executor.StopAgentSession(t.Context(), machine, staleIntent); err == nil {
		t.Fatal("Guest accepted a late old-generation revocation")
	}
	checkBinding(reconnected, "ready")
	shot(agentA, lease)
	if exec("cat /home/"+owned[0]+"/acceptance-input") != marker {
		t.Fatal("same-Lease reconnect lost the original Home")
	}
	target = reconnected
	t.Log("same-Lease real xrdp reconnect preserved UID/Home/deadline; old-generation login/revoke were rejected")
	// A fresh MCP server instance has no cached identity/session authority. Existing
	// leases continue to work only with newly validated credentials.
	_ = agentA.Close()
	_ = agentB.Close()
	endpoint.Close()
	endpoint = testMCPHTTPServer(t, controlPlane.URL, internalToken)
	agentA = testMCPClient(t, endpoint.URL+"/mcp", tokens[0])
	shot(agentA, lease)
	rotatedToken := "vcwa_rotated_" + suffix
	if _, err := db.RotateAgentToken(t.Context(), agentIDs[0], auth.TokenDigest(rotatedToken)); err != nil {
		t.Fatal(err)
	}
	liveMCPDenied(t, agentA, "desktop_screenshot", screenshotInput{LeaseID: lease.ID, ControlEpoch: lease.ControlEpoch})
	agentA = testMCPClient(t, endpoint.URL+"/mcp", rotatedToken)
	shot(agentA, lease)
	liveMCPCall(t, agentA, "desktop_release", leaseInput{LeaseID: lease.ID}, nil)
	liveMCPWaitDisabled(t, db, agentIDs[0], vmid)
	checkBinding(target, "disabled")
	exec("! pgrep -u " + owned[0] + " && getent shadow " + owned[0] + " | awk -F: '$2 ~ /^!/ && $8 == 1 {found=1} END {exit !found}'")
	liveMCPDenied(t, agentA, "desktop_screenshot", screenshotInput{LeaseID: lease.ID, ControlEpoch: lease.ControlEpoch})
	previousEpoch := lease.ControlEpoch
	liveMCPCall(t, agentA, "desktop_acquire", acquireInput{DesktopVMID: vmid, TTLSeconds: 300}, &lease)
	if lease.ControlEpoch <= previousEpoch+1 {
		t.Fatal("reacquisition reused the previous grant/revocation epoch")
	}
	shot(agentA, lease)
	reacquired, err := executor.DiscoverSession(t.Context(), machine, owned[0])
	if err != nil || reacquired.UID != target.UID || reacquired.InstanceID == target.InstanceID || exec("cat /home/"+owned[0]+"/acceptance-input") != marker {
		t.Fatal("reacquisition did not preserve Home/UID and renew Helper", err)
	}
	checkBinding(reacquired, "ready")
	if _, err := db.SetAgentEnabled(t.Context(), agentIDs[0], false); err != nil {
		t.Fatal(err)
	}
	liveMCPDenied(t, agentA, "desktop_screenshot", screenshotInput{LeaseID: lease.ID, ControlEpoch: lease.ControlEpoch})
	liveMCPWaitDisabled(t, db, agentIDs[0], vmid)
	checkBinding(reacquired, "disabled")
	exec("! pgrep -u " + owned[0])
	t.Log("MCP restart, credential rotation, release/reacquire and background disable convergence passed")
	assign(agentIDs[1])
	agentB = testMCPClient(t, endpoint.URL+"/mcp", tokens[1])
	liveMCPCall(t, agentB, "desktop_acquire", acquireInput{DesktopVMID: vmid, TTLSeconds: 300}, &lease)
	shot(agentB, lease)
	other, err := executor.DiscoverSession(t.Context(), machine, owned[1])
	if err != nil || other.UID == target.UID || other.Username != owned[1] {
		t.Fatal("second Agent did not get a separate OS identity", err)
	}
	checkBinding(other, "ready")
	exec("! runuser -u " + owned[1] + " -- cat /home/" + owned[0] + "/acceptance-input 2>/dev/null")
	if _, err := db.DeleteDesktopAssignment(t.Context(), "agent", agentIDs[1], vmid); err != nil {
		t.Fatal(err)
	}
	liveMCPDenied(t, agentB, "desktop_screenshot", screenshotInput{LeaseID: lease.ID, ControlEpoch: lease.ControlEpoch})
	liveMCPWaitDisabled(t, db, agentIDs[1], vmid)
	checkBinding(other, "disabled")
	exec("! pgrep -u " + owned[1])
	audit, err := db.AuditEvents(t.Context(), store.AuditEventQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	succeeded, denied := 0, 0
	operations := map[string]int{}
	for _, event := range audit.Events {
		if event.EventType == "agent.computer_action_succeeded" {
			if !slices.Contains(agentIDs, event.ActorID) {
				t.Fatal("computer action audit has an incorrect actor")
			}
			succeeded++
			var detail struct{ Operation string }
			if err := json.Unmarshal(event.Detail, &detail); err != nil {
				t.Fatal("invalid computer action audit metadata")
			}
			operations[detail.Operation]++
		}
		if event.EventType == "agent.computer_action_denied" {
			denied++
		}
		for _, secret := range []string{marker, tokens[0], tokens[1], rotatedToken, "\"data\""} {
			if strings.Contains(string(event.Detail), secret) {
				t.Fatal("private task, screenshot or credential content leaked into audit")
			}
		}
	}
	for _, operation := range []string{"screenshot", "accessibility_snapshot", "mouse", "key", "type_text"} {
		if operations[operation] == 0 {
			t.Fatalf("missing successful %s audit event", operation)
		}
	}
	if operations["screenshot"] < 6 || denied < 3 {
		t.Fatalf("missing successful/denied action audit events: %d/%d", succeeded, denied)
	}
	t.Logf("two isolated OS users, assignment revocation and redacted audit passed: %d successful / %d denied computer actions", succeeded, denied)
}

func liveMCPCall(t *testing.T, session *mcp.ClientSession, name string, input, output any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: input})
	if err != nil {
		t.Fatalf("MCP %s transport failed: %v", name, err)
	}
	if result.IsError {
		for _, content := range result.Content {
			if text, ok := content.(*mcp.TextContent); ok {
				t.Logf("MCP sanitized tool error: %.512s", text.Text)
			}
		}
		t.Fatalf("MCP %s tool failed: %v", name, result.GetError())
	}
	if output != nil {
		data, err := json.Marshal(result.StructuredContent)
		if err != nil || json.Unmarshal(data, output) != nil {
			t.Fatalf("MCP %s has invalid structured output", name)
		}
	}
	return result
}

func liveMCPDenied(t *testing.T, session *mcp.ClientSession, name string, input any) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: input})
	if err == nil && !result.IsError {
		t.Fatalf("MCP %s accepted unauthorized or stale input", name)
	}
	if result != nil {
		for _, content := range result.Content {
			if _, ok := content.(*mcp.ImageContent); ok {
				t.Fatal("denied call returned an observation")
			}
		}
	}
}

func liveMCPWaitDisabled(t *testing.T, db *store.Store, agentID string, vmid int) {
	t.Helper()
	// The real maintenance scheduler scans every minute. Do not call internal
	// reconciliation methods here: acceptance must cover the production loop.
	deadline := time.Now().Add(85 * time.Second)
	for {
		bindings, err := db.AgentGuestSessions(t.Context(), vmid)
		if err != nil {
			t.Fatal(err)
		}
		if slices.ContainsFunc(bindings, func(binding store.AgentGuestSession) bool {
			return binding.AgentID == agentID && binding.State == "disabled"
		}) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("production background revocation did not converge")
		}
		select {
		case <-t.Context().Done():
			t.Fatal("acceptance canceled")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func liveMCPPVEClient(t *testing.T) *pve.Client {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_PVE_ENDPOINT"))
	credentialFile := strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE"))
	if endpoint == "" || credentialFile == "" {
		t.Fatal("explicit PVE endpoint and credential file required")
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

func liveMCPMachine(t *testing.T, client *pve.Client, vmid int) pve.VM {
	t.Helper()
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, machine := range summary.VMs {
		if machine.VMID != vmid {
			continue
		}
		if machine.Kind != "qemu" || machine.Template || machine.Status != "running" || (!slices.Contains(machine.Tags, "vc-workspace") && !slices.Contains(machine.Tags, "vc-vdi")) {
			t.Fatal("explicit running, tagged, non-template desktop required")
		}
		config, err := client.VMConfiguration(t.Context(), machine.Node, vmid)
		if err != nil || config.OSType != "l26" {
			t.Fatal("this acceptance test requires a Linux desktop", err)
		}
		return machine
	}
	t.Fatal("acceptance VM not found")
	return pve.VM{}
}
