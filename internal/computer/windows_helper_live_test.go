package computer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/rdpsession"
)

//go:embed testdata/windows-helper/*.ps1
var windowsHelperFixtures embed.FS

// Real interactive acceptance, deliberately separate from SYSTEM-only native
// primitive tests. Requires an initially stopped, managed Windows test VM with
// no existing V2 registrations. Never replaces the installed agent or stores
// login passwords in argv, task definitions, files, logs or environment.
func TestLiveWindowsInteractiveHelper(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_HELPER") != "true" {
		t.Skip("opt-in real Windows RDP/two-user Helper acceptance")
	}
	runWindowsHelperAcceptance(t, true)
}

// Focused lifecycle evidence, not a substitute for input/MCP acceptance. It
// still uses real RDP logons and independent SAM/WTS checks; no forced session
// teardown or fabricated identity replaces a failed expiry assertion.
func TestLiveWindowsAccountExpiry(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_ACCOUNT_EXPIRY") != "true" {
		t.Skip("opt-in real Windows RDP account-expiry acceptance")
	}
	runWindowsHelperAcceptance(t, false)
}

func TestLiveWindowsAccountLease(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_ACCOUNT_FENCE") != "true" {
		t.Skip("opt-in versioned account/credential lifecycle with real RDP")
	}
	runWindowsHelperAcceptance(t, false)
}

func runWindowsHelperAcceptance(t *testing.T, checkInput bool) {
	t.Helper()
	fenced := os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_ACCOUNT_FENCE") == "true"
	if fenced && checkInput {
		t.Fatal("versioned account acceptance is separate from the GUI input matrix")
	}
	if executable := os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_SESSION_WORKER"); executable != "" {
		info, err := os.Stat(executable)
		if err != nil || !filepath.IsAbs(executable) || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 || info.Size() > 32*1024*1024 {
			t.Fatal("explicit trusted session worker executable required")
		}
		data, err := os.ReadFile(executable)
		if err != nil {
			t.Fatal(err)
		}
		// Freeze the tested binary before any VM mutation. A concurrent local
		// rebuild must not silently change the second user's transport.
		frozen := filepath.Join(t.TempDir(), "vc-workspace-session-worker")
		if err := os.WriteFile(frozen, data, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("VC_WORKSPACE_LIVE_WINDOWS_SESSION_WORKER", frozen)
		t.Logf("frozen headless session worker SHA256 %x", sha256.Sum256(data))
	}
	vmid, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_COMPUTER_VMID"))
	if err != nil || vmid <= 0 {
		t.Fatal("explicit Windows acceptance VMID required")
	}
	artifact, err := os.ReadFile(os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_HELPER_BINARY"))
	if err != nil || !windowsHelperPE(artifact) {
		t.Fatal("explicit bounded Windows amd64 Guest executable required")
	}
	bind, err := netip.ParseAddrPort(os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_HELPER_ARTIFACT_BIND"))
	if err != nil || !bind.Addr().IsPrivate() || !bind.Addr().Is4() {
		t.Fatal("explicit private IPv4 artifact bind address required, e.g. LAN-IP:0")
	}
	client := liveComputerClient(t)
	machine := liveComputerMachine(t, client, vmid)
	configuration, err := client.VMConfiguration(t.Context(), machine.Node, vmid)
	if err != nil || !strings.HasPrefix(configuration.OSType, "win") || machine.Status != "stopped" {
		t.Fatal("requires an initially stopped managed Windows acceptance VM", err)
	}
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal("read acceptance node memory before starting VM", err)
	}
	var node pve.Node
	for _, candidate := range summary.Nodes {
		if candidate.Name == machine.Node {
			node = candidate
		}
	}
	if err := nativeAcceptanceHeadroom(node, configuration.MemoryMB); err != nil {
		t.Fatal(err)
	}
	upid, err := client.ChangePowerState(t.Context(), machine.Node, vmid, "start")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		upid, err := client.ChangePowerState(ctx, machine.Node, vmid, "shutdown")
		if err != nil {
			t.Error("restore stopped Windows VM", err)
			return
		}
		waitLiveComputerTaskContext(t, ctx, client, machine.Node, upid)
		t.Logf("restored VM %d to stopped state", vmid)
	})
	waitLiveComputerTask(t, client, machine.Node, upid)
	waitLiveComputerQGA(t, client, machine)
	guestAddress := windowsTestGuestAddress(t, client, machine)
	marker := windowsHelperRandom(t, 12)
	stage := `C:\ProgramData\vcw-helper-test-` + marker
	t.Logf("VM %d: private fixture %s", vmid, stage)
	scripts := map[string]string{}
	for _, name := range []string{"desktop.ps1"} {
		data, err := windowsHelperFixtures.ReadFile("testdata/windows-helper/" + name)
		if err != nil {
			t.Fatal(err)
		}
		scripts[name] = base64.StdEncoding.EncodeToString(data)
	}
	fixtureScript, err := windowsHelperFixtures.ReadFile("testdata/windows-helper/fixture.ps1")
	if err != nil {
		t.Fatal(err)
	}
	passwords := []string{"Aa1!" + windowsHelperRandom(t, 20), "Aa1!" + windowsHelperRandom(t, 20)}
	users := []map[string]string{
		{"name": "vca" + windowsHelperRandom(t, 6), "password": passwords[0]},
		{"name": "vca" + windowsHelperRandom(t, 6), "password": passwords[1]},
	}
	redact := func(value string) string {
		for _, password := range passwords {
			value = strings.ReplaceAll(value, password, "[redacted]")
		}
		if len(value) > 4096 {
			value = value[:4096]
		}
		return value
	}
	fixture := func(ctx context.Context, operation string, extra map[string]any) (pve.GuestExecResult, error) {
		if extra == nil {
			extra = map[string]any{}
		}
		extra["operation"], extra["marker"] = operation, marker
		input, err := json.Marshal(extra)
		if err != nil {
			return pve.GuestExecResult{}, err
		}
		return client.ExecGuestWithInput(ctx, machine.Node, vmid, windowsPowerShell(string(fixtureScript)), input)
	}
	// Register cleanup before account creation, including lost QGA responses.
	// The remote fixture requires a matching SYSTEM-owned manifest and Agent
	// journal/SID receipts before removing accounts, including partial setup.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		result, err := fixture(ctx, "cleanup", map[string]any{"expected_users": []string{users[0]["name"], users[1]["name"]}})
		if err != nil || result.ExitCode != 0 || !windowsHelperCleanupReceipt(result.Stdout) {
			t.Errorf("fixture cleanup needs inspection at %s: exit=%d transport=%v stderr=%s", stage, result.ExitCode, err, redact(result.Stderr))
		} else {
			proof, proofErr := fixture(ctx, "inspect", map[string]any{"expected_users": []string{users[0]["name"], users[1]["name"]}})
			if proofErr != nil || proof.ExitCode != 0 || strings.TrimSpace(proof.Stdout) != "fixture-absent-and-no-owned-accounts-or-tasks" {
				t.Errorf("cleanup receipt lacks independent resource confirmation: exit=%d transport=%v stderr=%s", proof.ExitCode, proofErr, redact(proof.Stderr))
			} else {
				t.Log("independently confirmed fixture accounts, registry identities, tasks and staging are absent")
			}
		}
	})
	artifactURL := serveWindowsTestArtifact(t, bind, guestAddress, marker, "guest.exe", artifact)
	hash := fmt.Sprintf("%x", sha256.Sum256(artifact))
	result, err := fixture(t.Context(), "prepare", map[string]any{
		"users": users, "scripts": scripts, "artifact_url": artifactURL, "length": len(artifact), "sha256": hash, "fenced": fenced,
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("prepare fixture: exit=%d transport=%v stderr=%s", result.ExitCode, err, redact(result.Stderr))
	}
	var prepared struct {
		Stage       string                           `json:"stage"`
		Computer    string                           `json:"computer_name"`
		Fingerprint string                           `json:"certificate_sha256"`
		Users       []struct{ Username, SID string } `json:"users"`
		OSVersion   string                           `json:"os_version"`
		QGA         []struct {
			SessionID int    `json:"session_id"`
			Version   string `json:"file_version"`
		} `json:"qga"`
	}
	if json.Unmarshal([]byte(result.Stdout), &prepared) != nil || prepared.Stage != stage ||
		len(prepared.Users) != 2 || !regexp.MustCompile(`^[A-Za-z0-9-]{1,15}$`).MatchString(prepared.Computer) ||
		!regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(prepared.Fingerprint) {
		t.Logf("fixture receipt diagnostics: bytes=%d, stdout=%s, stderr=%s", len(result.Stdout), redact(result.Stdout), redact(result.Stderr))
		t.Fatal("invalid Windows fixture receipt")
	}
	t.Logf("isolated Windows build: %.80s", prepared.OSVersion)
	for _, qga := range prepared.QGA[:min(len(prepared.QGA), 4)] {
		t.Logf("isolated QGA file version: %.80s; process session=%d", qga.Version, qga.SessionID)
	}
	// Account/profile preparation can delay delivery of its receipt. Sample a
	// separate read-only command and bound clock skew across the entire RTT,
	// instead of misclassifying QGA delivery latency as Guest clock drift.
	clockOK := false
	for attempt := 0; attempt < 3; attempt++ {
		before := time.Now().UnixMilli()
		clockResult, clockErr := client.ExecGuest(t.Context(), machine.Node, vmid, windowsPowerShell("[DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()"))
		after := time.Now().UnixMilli()
		guestNow, parseErr := strconv.ParseInt(strings.TrimSpace(clockResult.Stdout), 10, 64)
		if clockErr == nil && clockResult.ExitCode == 0 && parseErr == nil && windowsHelperClockWithinBounds(before, after, guestNow) {
			t.Logf("Guest clock skew bounded by [%d, %d] ms, QGA RTT %d ms", guestNow-after, guestNow-before, after-before)
			clockOK = true
			break
		}
	}
	if !clockOK {
		t.Fatal("Guest clock skew cannot be bounded within 3 seconds; original action deadlines remain enforced")
	}
	cli := func(ctx context.Context, operation, username string, payload any, options ...string) (pve.GuestExecResult, error) {
		args := []string{stage + `\guest.exe`, "computer-v2-" + operation}
		if username != "" {
			args = append(args, "--guest-user", username)
		}
		args = append(args, options...)
		if payload == nil {
			if operation == "capabilities" || operation == "account-inspect" || operation == "session" {
				return readWindowsGuestResult(ctx, func(call context.Context) (pve.GuestExecResult, error) {
					return client.ExecGuest(call, machine.Node, vmid, args)
				})
			}
			return client.ExecGuest(ctx, machine.Node, vmid, args)
		}
		input, err := json.Marshal(payload)
		if err != nil {
			return pve.GuestExecResult{}, err
		}
		return client.ExecGuestWithInput(ctx, machine.Node, vmid, args, input)
	}
	windowsExecutor := NewWindowsSessionExecutor(windowsFixtureObservedGuest{client, t, redact})
	// Only the fixture selects its frozen, private staging binary. Production
	// construction always uses the fixed installed Agent path.
	windowsExecutor.binary = stage + `\guest.exe`
	for index, user := range prepared.Users {
		if user.Username != users[index]["name"] || !validWindowsAccountSID(user.SID) {
			t.Fatal("fixture account SID mismatch")
		}
		t.Logf("owned fixture identity: %s SID %s", user.Username, user.SID)
		accountState, stateErr := windowsExecutor.ObserveWindowsAgentAccount(t.Context(), machine, user.Username, user.SID)
		if stateErr != nil || !accountState.Exists || len(accountState.Sessions) != 0 {
			t.Fatal("owned account observation before RDP login failed", stateErr)
		}
		// helper-start itself is idempotent under the owned-account lifecycle
		// gate (also exercised by the repeated ready loop below). An uncertain
		// QGA result is not rejection evidence; require an actual nonzero exit.
		// This fixed test operation is not a generic lifecycle retry wrapper.
		probe, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		var result pve.GuestExecResult
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			result, err = cli(probe, "helper-start", user.Username, nil)
			if err == nil || !windowsTestQGAResultUncertain(err) || probe.Err() != nil {
				break
			}
			t.Log("uncertain idempotent Helper-start result; still requiring explicit rejection without a logon")
			select {
			case <-probe.Done():
			case <-time.After(250 * time.Millisecond):
			}
		}
		cancel()
		if err != nil || result.ExitCode == 0 {
			t.Fatal("Helper start without a real logon was not explicitly rejected", err)
		}
	}
	result, err = fixture(t.Context(), "tasks", nil)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("register interactive fixture tasks: exit=%d transport=%v stderr=%s", result.ExitCode, err, redact(result.Stderr))
	}
	lease := "lease_" + marker
	var previous SessionTarget
	for index, user := range prepared.Users {
		t.Logf("RDP login %d/2 as %s; certificate SHA256 pinned", index+1, user.Username)
		var accountLease WindowsAccountLease
		if fenced {
			// Start the immutable login window immediately before this user's
			// real RDP login. Never shorten/forge a journal to simulate expiry.
			accountLease = WindowsAccountLease{SchemaVersion: 1, Identity: WindowsAccountIdentity{Username: user.Username, SID: user.SID},
				LeaseID: lease, ControlEpoch: int64(index + 1), LoginGeneration: 1, ExpiresUnixSeconds: time.Now().Add(5 * time.Minute).Unix()}
			secret := []byte(passwords[index])
			_, leaseErr := windowsExecutor.ChangeWindowsAccountLease(t.Context(), machine, accountLease, "open", secret)
			clear(secret)
			if leaseErr != nil {
				t.Fatal("versioned login window failed; mutation not replayed", leaseErr)
			}
		}
		stop, running := startWindowsHelperRDP(t, marker, index, guestAddress, prepared.Computer, user.Username, passwords[index], prepared.Fingerprint)
		var target SessionTarget
		var warming SessionTarget
		readyObservations := 0
		deadline := time.Now().Add(4 * time.Minute)
		for time.Now().Before(deadline) {
			running()
			result, err = cli(t.Context(), "helper-start", user.Username, nil)
			var announcement struct {
				SchemaVersion int           `json:"schema_version"`
				Target        SessionTarget `json:"target"`
				InputReady    bool          `json:"input_ready"`
			}
			if err == nil && result.ExitCode == 0 && json.Unmarshal([]byte(result.Stdout), &announcement) == nil && announcement.SchemaVersion == 2 && announcement.Target.Validate() == nil {
				if warming.Username != "" && warming != announcement.Target {
					t.Fatal("Helper churned while waiting for the same user's desktop readiness")
				}
				if announcement.InputReady {
					readyObservations++
					if readyObservations == 2 {
						target = announcement.Target
						break
					}
				}
				if warming.Username == "" && !announcement.InputReady {
					t.Logf("%s: Helper alive; waiting for normal user input desktop", user.Username)
				}
				warming = announcement.Target
			}
			time.Sleep(time.Second)
		}
		if target.Username != user.Username || target.SID != user.SID {
			diagnostic, _ := fixture(t.Context(), "diagnostics", nil)
			t.Log("fixture task diagnostics: " + redact(diagnostic.Stdout))
			t.Fatalf("interactive Helper discovery failed: transport=%v stderr=%s", err, redact(result.Stderr))
		}
		if index == 1 && (target.SID == previous.SID || target.InstanceID == previous.InstanceID || target.SessionID == previous.SessionID) {
			t.Fatal("second login reused another user's authenticated identity")
		}
		// helper-start is explicitly idempotent under its lifecycle gate. The
		// bounded loop requires two actual ready receipts with the same full
		// identity; a lost QGA result is not a claim that no process was started.
		t.Logf("%s: Agent-launched Helper ready; repeat start preserved the same instance", user.Username)
		if err := windowsExecutor.InitializeSessionTransport(t.Context(), machine, user.Username); err != nil {
			t.Fatal("Windows executor ownership/initialization failed", err)
		}
		discovered, err := windowsExecutor.DiscoverSession(t.Context(), machine, user.Username)
		if err != nil || discovered != target {
			t.Fatal("Windows executor did not discover the exact live Helper identity", err)
		}
		accountState, stateErr := windowsExecutor.ObserveWindowsAgentAccount(t.Context(), machine, user.Username, user.SID)
		if stateErr != nil || !accountState.Exists || accountState.Disabled || len(accountState.Sessions) != 1 || accountState.Sessions[0] != target.SessionID || accountState.LoginStopped() {
			t.Fatal("SAM/WTS observation does not match the real authenticated Helper logon", stateErr)
		}
		if fenced {
			want := accountLease
			want.Phase = "open"
			if accountState.Lifecycle == nil || *accountState.Lifecycle != want {
				t.Fatal("account observation lost the exact open lease")
			}
			sealed, sealErr := windowsExecutor.ChangeWindowsAccountLease(t.Context(), machine, accountLease, "seal", nil)
			if sealErr != nil {
				t.Fatal("versioned bootstrap credential retirement failed; mutation not replayed", sealErr)
			}
			// Password retirement must preserve the existing real OS desktop.
			running()
			retained, retainErr := windowsExecutor.DiscoverSession(t.Context(), machine, user.Username)
			if retainErr != nil || retained != target {
				t.Fatal("retiring the login credential replaced/lost the real desktop", retainErr)
			}
			observed, observeErr := windowsExecutor.ObserveWindowsAgentAccount(t.Context(), machine, user.Username, user.SID)
			if observeErr != nil || observed.Lifecycle == nil || *observed.Lifecycle != sealed || len(observed.Sessions) != 1 || observed.Sessions[0] != target.SessionID {
				t.Fatal("sealed account did not retain the exact real WTS logon", observeErr)
			}
			t.Logf("%s: versioned open/seal receipts matched; bootstrap credential retired without replacing the RDP desktop", user.Username)
		}
		if checkInput {
			// Only this private acceptance lease includes human inspection time.
			// Individual action/replay/worker deadlines are never extended.
			authority := boundAuthority{2, &target, Authority{1, lease, int64(index + 1), "active", time.Now().Add(12 * time.Minute).UnixMilli()}}
			if err := windowsExecutor.ActivateSessionAuthority(t.Context(), machine, target, authority.Authority); err != nil {
				t.Fatal("activate exact Helper authority through Windows executor", err)
			}
			makeAction := func(request Request) boundAction {
				request.SchemaVersion, request.RequestID, request.LeaseID = 1, "action_"+windowsHelperRandom(t, 16), lease
				request.ControlEpoch, request.ExpiresUnixMS = int64(index+1), time.Now().Add(15*time.Second).UnixMilli()
				return boundAction{2, target, 15000, request}
			}
			dispatch := func(action boundAction, allowed bool) Response {
				t.Helper()
				if action.Request.ExpiresUnixMS <= time.Now().UnixMilli() {
					t.Fatal("action expired before dispatch; cannot claim replay or authorization coverage")
				}
				if allowed {
					response, err := windowsExecutor.ExecuteForSession(t.Context(), machine, action.Target, action.Request, time.Duration(action.TimeoutMS)*time.Millisecond)
					if err != nil {
						t.Fatalf("Windows executor interactive %s failed: %v", action.Request.Operation, err)
					}
					return response
				}
				// Invalid/revoked requests must still reach the Guest boundary. A
				// server-side validation failure is not evidence of Guest isolation.
				result, err := replayWindowsHelperAction(t.Context(), action, func(ctx context.Context, payload []byte) (pve.GuestExecResult, error) {
					return client.ExecGuestWithInput(ctx, machine.Node, vmid, []string{stage + `\guest.exe`, "computer-v2-dispatch", "--guest-user", user.Username, "--request-id", action.Request.RequestID}, payload)
				})
				if err != nil {
					t.Fatal("dispatch transport failure (not an authorization rejection)", err)
				}
				if result.ExitCode == 0 {
					t.Fatal("invalid or revoked action was accepted")
				}
				if action.Request.ExpiresUnixMS <= time.Now().UnixMilli()+3000 || strings.Contains(result.Stderr, "deadline") || strings.Contains(result.Stderr, "expired") {
					t.Fatal("rejection may be caused by timeout/expiry instead of the tested authorization boundary")
				}
				return Response{}
			}
			assist := func() {
				assistWindowsHelperDesktop(t, func(parent context.Context) *ScreenshotResult {
					// Shell/OOBE initialization can temporarily stall QGA. Only
					// this read-only readiness probe may create a fresh read after
					// expiry; it never changes the target or retries an input.
					ctx, cancel := context.WithTimeout(parent, time.Minute)
					defer cancel()
					for attempt := 0; attempt < 4 && ctx.Err() == nil; attempt++ {
						action := makeAction(Request{Operation: OperationScreenshot, Screenshot: &Screenshot{MaxWidth: 1280}})
						result, err := replayWindowsHelperAction(ctx, action, func(call context.Context, payload []byte) (pve.GuestExecResult, error) {
							return client.ExecGuestWithInput(call, machine.Node, vmid, []string{stage + `\guest.exe`, "computer-v2-dispatch", "--guest-user", user.Username, "--request-id", action.Request.RequestID}, payload)
						})
						if err != nil && (errors.Is(err, context.DeadlineExceeded) || windowsTestQGAResultUncertain(err)) {
							t.Log("read-only readiness screenshot unavailable; retry within the same bound identity and one-minute window")
							select {
							case <-ctx.Done():
							case <-time.After(500 * time.Millisecond):
							}
							continue
						}
						var response Response
						if err != nil || result.ExitCode != 0 || json.Unmarshal([]byte(result.Stdout), &response) != nil || !response.OK || response.SchemaVersion != 1 || response.RequestID != action.Request.RequestID || response.Screenshot == nil {
							t.Fatalf("readiness screenshot failed; no input or authorization retry: transport=%v exit=%d stderr=%s", err, result.ExitCode, redact(result.Stderr))
						}
						return response.Screenshot
					}
					t.Fatal("desktop readiness screenshot deadline exceeded")
					return nil
				}, func(x, y int) {
					dispatch(makeAction(Request{Operation: OperationMouse, Mouse: &Mouse{Action: "click", Button: "left", X: x, Y: y}}), true)
				})
			}
			assistanceEnabled := os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_HELPER_DESKTOP_ASSIST") == "true"
			if assistanceEnabled {
				assist()
			}
			// First-login setup can block the separate WinForms application even
			// though the normal input desktop is available. Inspect/assist that
			// exact authenticated desktop before waiting for the Shown receipt.
			// Assistance never substitutes for this independent visibility gate
			// or the subsequent real foreground, mouse and text assertions.
			deadline = time.Now().Add(45 * time.Second)
			visible := false
			for time.Now().Before(deadline) {
				result, err = fixture(t.Context(), "desktop-ready", map[string]any{"username": user.Username, "sid": user.SID})
				if err != nil && windowsTestQGAResultUncertain(err) {
					// Read-only owned Shown marker; no process launch or setup retry.
					time.Sleep(250 * time.Millisecond)
					continue
				}
				if err != nil || result.ExitCode != 0 {
					t.Fatal("read fixture application readiness", err, result.ExitCode, redact(result.Stderr))
				}
				if strings.TrimSpace(result.Stdout) == "fixture-visible" {
					visible = true
					break
				}
				time.Sleep(time.Second)
			}
			if !visible {
				diagnostic, _ := fixture(t.Context(), "diagnostics", nil)
				t.Fatal("fixture application did not become visible: " + redact(diagnostic.Stdout))
			}
			screenshot := dispatch(makeAction(Request{Operation: OperationScreenshot, Screenshot: &Screenshot{MaxWidth: 640}}), true).Screenshot
			if screenshot == nil || screenshot.ContentType != "image/jpeg" || screenshot.DesktopBounds == nil || screenshot.Width != 640 || screenshot.DesktopBounds.Width < 640 {
				t.Fatal("invalid session screenshot metadata")
			}
			pixels, err := base64.StdEncoding.DecodeString(screenshot.Data)
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(pixels)) != screenshot.SHA256 {
				t.Fatal("screenshot integrity failure")
			}
			dimensions, err := jpeg.DecodeConfig(bytes.NewReader(pixels))
			if err != nil || dimensions.Width != screenshot.Width || dimensions.Height != screenshot.Height {
				t.Fatal("JPEG dimensions mismatch")
			}
			tree := func() []AccessibilityNode {
				response := dispatch(makeAction(Request{Operation: OperationAccessibility, Accessibility: &Accessibility{MaxDepth: 10, MaxNodes: 500}}), true)
				if response.Accessibility == nil || response.Accessibility.Source != "windows_uia" || len(response.Accessibility.Nodes) == 0 {
					t.Fatal("real session UI Automation unavailable")
				}
				return response.Accessibility.Nodes
			}
			var input AccessibilityNode
			nodes := tree()
			for _, node := range nodes {
				if index == 1 && strings.Contains(node.Name, previous.Username) {
					t.Fatal("another user's private desktop leaked into UIA")
				}
				if node.Name == "VCW_INPUT_"+user.Username {
					input = node
				}
			}
			if input.Width <= 0 || input.Height <= 0 {
				diagnostic, _ := fixture(t.Context(), "diagnostics", nil)
				t.Log("fixture task diagnostics: " + redact(diagnostic.Stdout))
				roles := map[string]int{}
				labels := 0
				for _, node := range nodes {
					roles[node.Role]++
					if node.Name != "" {
						labels++
					}
					if strings.HasPrefix(node.Name, "VCW") {
						t.Logf("public fixture node: %s bounds=%d,%d %dx%d", node.Name, node.X, node.Y, node.Width, node.Height)
					}
				}
				t.Logf("UIA diagnostic counts: nodes=%d named=%d roles=%v", len(nodes), labels, roles)
				t.Fatal("private fixture input is not visible in the user's UIA tree")
			}
			focused := false
			for attempt := 0; attempt < 2; attempt++ {
				if attempt > 0 {
					if !assistanceEnabled {
						break
					}
					t.Log("fixture input was occluded; inspect the current desktop before retrying foreground assertion")
					assist()
					input = AccessibilityNode{}
					for _, node := range tree() {
						if node.Name == "VCW_INPUT_"+user.Username {
							input = node
						}
					}
					if input.Width <= 0 || input.Height <= 0 {
						t.Fatal("fixture input disappeared after assistance")
					}
				}
				dispatch(makeAction(Request{Operation: OperationMouse, Mouse: &Mouse{Action: "click", Button: "left", X: input.X + input.Width/2, Y: input.Y + input.Height/2}}), true)
				for _, node := range tree() {
					if strings.HasPrefix(node.Name, "VCW_STATUS:") {
						t.Log("public fixture state: " + node.Name)
					}
					if node.Name == "VCW_INPUT_"+user.Username {
						focused = node.Focused
						t.Logf("public fixture input after click: focused=%t enabled=%t bounds=%d,%d %dx%d", node.Focused, node.Enabled, node.X, node.Y, node.Width, node.Height)
					}
				}
				if focused {
					break
				}
			}
			if !focused {
				// This is a synthetic, isolated test desktop, not an existing user's
				// session. Keep only the failed screenshot as a private QA artifact.
				current := dispatch(makeAction(Request{Operation: OperationScreenshot, Screenshot: &Screenshot{MaxWidth: 640}}), true).Screenshot
				saveWindowsFixtureScreenshot(t, current)
				t.Fatal("fixture input is not foreground; inspect the screenshot and complete supported Windows first-login setup before rerunning")
			}
			publicText := "VCW-" + user.Username + "-中文🙂"
			typing := makeAction(Request{Operation: OperationTypeText, Text: &Text{Value: publicText}})
			dispatch(typing, true)
			dispatch(typing, true) // Exact retry must not apply input twice.
			changed := typing
			changed.Request.Text = &Text{Value: "MUST-NOT-APPLY"}
			dispatch(changed, false)
			if !observeWindowsFixtureEcho(t, publicText, tree) {
				current := dispatch(makeAction(Request{Operation: OperationScreenshot, Screenshot: &Screenshot{MaxWidth: 1280}}), true).Screenshot
				saveWindowsFixtureScreenshot(t, current)
				t.Fatal("public fixture text did not converge exactly; no input was resent")
			}
			// Exercise literal braces, mixed case and supplementary Unicode across
			// many authority-checked chunks. UIA names remain below their 512-rune
			// privacy bound. No clipboard or direct control-value write is involved.
			dispatch(makeAction(Request{Operation: OperationKey, Key: &Key{Key: "a", Modifiers: []string{"control"}}}), true)
			selected := false
			for attempt := 0; attempt < 4; attempt++ {
				current := tree()
				for _, node := range current {
					if strings.HasPrefix(node.Name, "VCW_STATUS:") {
						t.Log("public selection evidence: " + node.Name)
					}
				}
				if windowsFixtureSelectionReady(current, publicText) {
					selected = true
					break
				}
				if attempt != 3 {
					time.Sleep(500 * time.Millisecond)
				}
			}
			if !selected {
				current := dispatch(makeAction(Request{Operation: OperationScreenshot, Screenshot: &Screenshot{MaxWidth: 1280}}), true).Screenshot
				saveWindowsFixtureScreenshot(t, current)
				t.Fatal("Ctrl+A did not select the exact public fixture text; no replacement text was sent")
			}
			longText := "VCW-long-" + strings.Repeat("Ab{c}(1)中🙂", 36)
			longTyping := makeAction(Request{Operation: OperationTypeText, Text: &Text{Value: longText}})
			dispatch(longTyping, true)
			dispatch(longTyping, true)
			if !observeWindowsFixtureEcho(t, longText, tree) {
				current := dispatch(makeAction(Request{Operation: OperationScreenshot, Screenshot: &Screenshot{MaxWidth: 1280}}), true).Screenshot
				saveWindowsFixtureScreenshot(t, current)
				t.Fatal("long literal Unicode input/replay did not converge exactly")
			}
			t.Logf("%s: literal multi-chunk text and exact replay passed (%d UTF-8 bytes)", user.Username, len(longText))
			wrong := makeAction(Request{Operation: OperationScreenshot, Screenshot: &Screenshot{MaxWidth: 640}})
			wrong.Target.InstanceID = windowsHelperRandom(t, 32)
			dispatch(wrong, false)
			if index == 1 {
				wrong.Target = previous
				dispatch(wrong, false)
			}
			// Stop only the exact authenticated Helper; the user's desktop and fixture
			// stay alive. Old authorization must not activate its replacement.
			oldTarget := target
			result, err = cli(t.Context(), "helper-stop", user.Username, oldTarget)
			if err != nil || result.ExitCode != 0 {
				t.Fatalf("restart fixture Helper: exit=%d transport=%v stderr=%s", result.ExitCode, err, redact(result.Stderr))
			}
			deadline = time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				result, err = cli(t.Context(), "helper-start", user.Username, nil)
				var announcement struct {
					SchemaVersion int           `json:"schema_version"`
					Target        SessionTarget `json:"target"`
				}
				if err == nil && result.ExitCode == 0 && json.Unmarshal([]byte(result.Stdout), &announcement) == nil && announcement.SchemaVersion == 2 && announcement.Target.Validate() == nil && announcement.Target.InstanceID != oldTarget.InstanceID {
					target = announcement.Target
					break
				}
				time.Sleep(250 * time.Millisecond)
			}
			if target.SID != oldTarget.SID || target.SessionID != oldTarget.SessionID || target.InstanceID == oldTarget.InstanceID {
				t.Fatal("Helper restart did not produce a new instance in the same user's logon session")
			}
			result, err = cli(t.Context(), "helper-stop", user.Username, oldTarget)
			if err != nil || result.ExitCode == 0 {
				t.Fatal("stale stop did not explicitly reject replacement Helper", err)
			}
			oldAuthority := authority
			oldAuthority.Target = &oldTarget
			result, err = cli(t.Context(), "authority", "", oldAuthority)
			if err != nil || result.ExitCode == 0 {
				t.Fatal("old Helper authorization was not explicitly rejected", err)
			}
			fresh := makeAction(Request{Operation: OperationScreenshot, Screenshot: &Screenshot{MaxWidth: 640}})
			dispatch(fresh, false)
			authority.Target = &target
			if err := windowsExecutor.ActivateSessionAuthority(t.Context(), machine, target, authority.Authority); err != nil {
				t.Fatal("replacement Helper authorization through Windows executor failed", err)
			}
			dispatch(fresh, true)
			// Make a fresh action immediately before revocation so denial cannot be
			// attributed merely to expiry of earlier test requests.
			revoked := makeAction(Request{Operation: OperationTypeText, Text: &Text{Value: "MUST-NOT-APPLY"}})
			authority.Target, authority.Authority.State, authority.Authority.ExpiresUnixMS = nil, "revoked", time.Now().Add(-time.Second).UnixMilli()
			if err := windowsExecutor.RevokeSessionAuthority(t.Context(), machine, authority.Authority); err != nil {
				t.Fatal("revoke fixture authority through Windows executor failed", err)
			}
			if revoked.Request.ExpiresUnixMS <= time.Now().UnixMilli() {
				t.Fatal("revocation test action expired before rejection check")
			}
			dispatch(revoked, false)
			t.Logf("%s: real SID/WTS/LUID, JPEG, UIA, mouse/text, exact retry, conflict, Helper restart and revocation passed", user.Username)
		}
		if index == 0 {
			if fenced {
				accountLease.ExpiresUnixSeconds = 0
				_, err = windowsExecutor.ChangeWindowsAccountLease(t.Context(), machine, accountLease, "revoke", nil)
				result = pve.GuestExecResult{}
			} else {
				result, err = cli(t.Context(), "account-disable", user.Username, nil)
			}
		} else {
			// Keep the real RDP transport open. Account expiry alone does not
			// terminate a logged-on desktop; exercise the same local sweep as
			// the SYSTEM heartbeat, without invoking account-disable remotely.
			expires := accountLease.ExpiresUnixSeconds
			if !fenced {
				expires = time.Now().Add(30 * time.Second).Unix()
				result, err = cli(t.Context(), "account-enable", user.Username, map[string]any{"expires_unix_seconds": expires})
				if err != nil || result.ExitCode != 0 {
					t.Fatal("bound local-expiry fixture window failed", err, result.ExitCode)
				}
			}
			t.Log("waiting for owned account login expiry while real RDP remains connected")
			select {
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			case <-time.After(max(time.Duration(0), time.Until(time.Unix(expires+4, 0)))):
			}
			result, err = cli(t.Context(), "accounts-reconcile", "", nil)
		}
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("close only fixture SID: exit=%d transport=%v stderr=%s", result.ExitCode, err, redact(result.Stderr))
		}
		// A separate read observes actual SAM and kernel logons, not the disable
		// command's reply, Helper readiness, or an English error substring.
		accountState, stateErr = windowsExecutor.ObserveWindowsAgentAccount(t.Context(), machine, user.Username, user.SID)
		if stateErr != nil || !accountState.Exists || !accountState.Disabled || !accountState.LoginStopped() {
			t.Fatal("independent typed SAM disable/WTS absence confirmation failed", stateErr)
		}
		if fenced && (accountState.Lifecycle == nil || accountState.Lifecycle.Phase != "revoked" || accountState.Lifecycle.LeaseID != lease || accountState.Lifecycle.ControlEpoch != int64(index+1)) {
			t.Fatal("account cleanup lost the durable terminal lease version")
		}
		t.Logf("%s: account disabled and independent WTS absence confirmed (local expiry=%t)", user.Username, index == 1)
		stop()
		previous = target
	}
	if checkInput {
		t.Logf("Windows Guest SHA256 %s: two sequential real-user sessions passed; not a multi-session or production bootstrap claim", hash)
	} else {
		t.Logf("Windows Guest SHA256 %s: real RDP explicit disable and local-expiry/WTS absence passed; NO input, MCP or unattended-template acceptance", hash)
	}
}

func saveWindowsFixtureScreenshot(t *testing.T, current *ScreenshotResult) {
	t.Helper()
	if current == nil || current.ContentType != "image/jpeg" {
		t.Fatal("missing failure-time screenshot")
	}
	pixels, err := base64.StdEncoding.DecodeString(current.Data)
	if err != nil || len(pixels) > 16*1024*1024 || fmt.Sprintf("%x", sha256.Sum256(pixels)) != current.SHA256 {
		t.Fatal("failure-time screenshot integrity mismatch")
	}
	file, err := os.CreateTemp("", "vcw-windows-helper-failure-*.jpg")
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(pixels)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("save private fixture screenshot", writeErr, closeErr)
	}
	t.Log("private fixture screenshot: " + file.Name())
}

func windowsFixtureSelectionReady(nodes []AccessibilityNode, expected string) bool {
	count, matched := 0, false
	want := fmt.Sprintf(";selection=0,%d;", len(utf16.Encode([]rune(expected))))
	for _, node := range nodes {
		if strings.HasPrefix(node.Name, "VCW_STATUS:") {
			count++
			matched = strings.Contains(node.Name, want)
		}
	}
	return count == 1 && matched
}

func TestWindowsFixtureUsesFrameworkShortcutsWithoutEditingValues(t *testing.T) {
	source, err := windowsHelperFixtures.ReadFile("testdata/windows-helper/desktop.ps1")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"[AppContext]::SetSwitch('Switch.System.Windows.Forms.DoNotSupportSelectAllShortcutInMultilineTextBox',$false)",
		"$inputBox.Multiline=$true", "$inputBox.ShortcutsEnabled=$true",
	} {
		if !strings.Contains(text, required) {
			t.Fatal("test application must opt into the framework's built-in selection shortcut")
		}
	}
	// Neither the test's UI handlers nor an input convenience method may
	// manufacture the text/selection that the real Guest action must produce.
	for _, forbidden := range []string{
		`(?i)\$inputBox\.(Text|SelectedText|SelectionStart|SelectionLength)\s*=`,
		`(?i)\$inputBox\.(Select|SelectAll|Paste|Clear)\s*\(`,
		`(?i)(SendKeys|SendMessage|PostMessage|SendInput|keybd_event)\s*[.:\(]`,
	} {
		if regexp.MustCompile(forbidden).Match(source) {
			t.Fatal("test application may not inject input or assign the tested value/selection")
		}
	}
}

func TestWindowsFixtureSelectionRequiresExactUTF16Range(t *testing.T) {
	for _, value := range []string{"VCW_STATUS:focused=True;selection=0,4;keys=2", "VCW_STATUS:focused=True;selection=0,3;keys=2", "VCW_STATUS:focused=True;selection=0,40;keys=2", "VCW_STATUS:focused=True;selection=1,4;keys=2"} {
		got := windowsFixtureSelectionReady([]AccessibilityNode{{Name: value}}, "a中🙂")
		if got != strings.Contains(value, "selection=0,4;") {
			t.Fatal("selection evidence misread")
		}
	}
	if windowsFixtureSelectionReady(nil, "a中🙂") {
		t.Fatal("absent selection accepted")
	}
}

// SendInput acknowledgement is queue insertion, not proof that an application
// has updated every UIA property. Read at most four fresh bounded snapshots;
// never type again. Require two consecutive exact observations. A wrong suffix
// or duplicate is a failure immediately, not something to edit or wait away.
func observeWindowsFixtureEcho(t *testing.T, expected string, read func() []AccessibilityNode) bool {
	t.Helper()
	exact := 0
	for attempt := 0; attempt < 4; attempt++ {
		value, count := "", 0
		for _, node := range read() {
			if strings.HasPrefix(node.Name, "VCW_ECHO:") {
				count++
				value = strings.TrimPrefix(node.Name, "VCW_ECHO:")
			}
			if strings.HasPrefix(node.Name, "VCW_STATUS:") || strings.HasPrefix(node.Name, "VCW_INPUT_") {
				t.Logf("public input completion node: name=%s focused=%t", node.Name, node.Focused)
			}
		}
		if count != 1 || !strings.HasPrefix(expected, value) {
			t.Logf("public input mismatch: echo_count=%d value=%q", count, value)
			return false
		}
		if value == expected {
			exact++
		} else {
			exact = 0
			t.Logf("public input completion pending: observation=%d value=%q", attempt+1, value)
		}
		if exact == 2 {
			return true
		}
		if attempt != 3 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	return false
}

func TestWindowsFixtureEchoObservationNeverReappliesInput(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []string
		want   bool
		reads  int
	}{
		{"stable", []string{"public", "public"}, true, 2},
		{"queued", []string{"pub", "public", "public"}, true, 3},
		{"truncated", []string{"pub", "pub", "pub", "pub"}, false, 4},
		{"duplicated", []string{"publicpublic"}, false, 1},
		{"conflict", []string{"MUST-NOT-APPLY"}, false, 1},
		{"unstable", []string{"public", "pub", "public", "pub"}, false, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			got := observeWindowsFixtureEcho(t, "public", func() []AccessibilityNode {
				if reads >= len(test.values) {
					t.Fatal("read budget exceeded")
				}
				value := test.values[reads]
				reads++
				return []AccessibilityNode{{Name: "VCW_ECHO:" + value}}
			})
			if got != test.want || reads != test.reads {
				t.Fatal("wrong completion evidence", got, reads)
			}
		})
	}
}

func windowsHelperCleanupReceipt(output string) bool {
	return strings.TrimSpace(output) == "fixture-cleaned" || strings.TrimSpace(output) == "fixture-absent-and-no-owned-accounts-or-tasks"
}

func TestWindowsHelperCleanupRequiresExplicitReceipt(t *testing.T) {
	for _, value := range []string{"", "ok", "fixture-cleaned\nextra", "fixture-cleaned-partial"} {
		if windowsHelperCleanupReceipt(value) {
			t.Fatal("unproven cleanup acknowledged")
		}
	}
	for _, value := range []string{"fixture-cleaned\r\n", "fixture-absent-and-no-owned-accounts-or-tasks"} {
		if !windowsHelperCleanupReceipt(value) {
			t.Fatal("valid cleanup receipt rejected")
		}
	}
}

func windowsHelperRandom(t *testing.T, length int) string {
	t.Helper()
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value)
}

// Only this isolated fixture observes failed Guest diagnostics. Production
// errors expose fixed stages/exit codes, never stderr or desktop contents.
// Do not print argv, stdin or a successful screenshot/UIA response.
type windowsFixtureObservedGuest struct {
	*pve.Client
	t      *testing.T
	redact func(string) string
}

func (g windowsFixtureObservedGuest) observe(args []string, result pve.GuestExecResult, err error) (pve.GuestExecResult, error) {
	if err != nil || result.ExitCode != 0 {
		op := "unknown"
		if len(args) > 1 && strings.HasPrefix(args[1], "computer-v2-") {
			op = args[1]
		}
		g.t.Logf("isolated Windows Guest failure: operation=%s exit=%d transport=%v stderr=%s", op, result.ExitCode, err, g.redact(result.Stderr))
	}
	return result, err
}

func (g windowsFixtureObservedGuest) ExecGuest(ctx context.Context, node string, vmid int, args []string) (pve.GuestExecResult, error) {
	result, err := g.Client.ExecGuest(ctx, node, vmid, args)
	return g.observe(args, result, err)
}

func (g windowsFixtureObservedGuest) ExecGuestWithInput(ctx context.Context, node string, vmid int, args []string, input []byte) (pve.GuestExecResult, error) {
	result, err := g.Client.ExecGuestWithInput(ctx, node, vmid, args, input)
	return g.observe(args, result, err)
}

// Only V2 actions have this contract: the authenticated Helper reserves the
// exact request before side effects and never reuses its instance ID after a
// restart. A retry cannot extend its deadline, change any bytes or discover a
// replacement Helper. This is NOT a retry wrapper for arbitrary QGA commands,
// authorization writes, account creation or fixture cleanup.
func replayWindowsHelperAction(ctx context.Context, action boundAction, send func(context.Context, []byte) (pve.GuestExecResult, error)) (pve.GuestExecResult, error) {
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(action.Request.ExpiresUnixMS).Add(-3*time.Second))
	defer cancel()
	return replayBoundWindowsAction(ctx, action, send)
}

// Classification only, not a generic retry policy. Callers must prove their
// operation is read-only or use the bound action's immutable replay contract.
func windowsTestQGAResultUncertain(err error) bool {
	return windowsGuestResultUncertain(err)
}

func TestWindowsHelperActionReplayKeepsBytesInstanceAndDeadline(t *testing.T) {
	action := boundAction{2, SessionTarget{"vca0123456789ab", 0, "S-1-5-21-1-2-3-1001", "windows:2:0000000000000001", strings.Repeat("a", 64)}, 1000,
		Request{SchemaVersion: 1, RequestID: "action_" + strings.Repeat("a", 32), LeaseID: "lease_fixture", ControlEpoch: 1, ExpiresUnixMS: time.Now().Add(15 * time.Second).UnixMilli(), Operation: OperationTypeText, Text: &Text{Value: "public fixture 中文🙂"}}}
	want, _ := pve.MarshalGuestJSON(action)
	attempts := 0
	_, err := replayWindowsHelperAction(t.Context(), action, func(ctx context.Context, payload []byte) (pve.GuestExecResult, error) {
		attempts++
		if !bytes.Equal(payload, want) {
			t.Fatal("retried action changed identity, content or deadline")
		}
		deadline, ok := ctx.Deadline()
		if !ok || deadline.After(time.UnixMilli(action.Request.ExpiresUnixMS)) {
			t.Fatal("retry extended action deadline")
		}
		if attempts == 1 {
			payload[0] = '!'
			return pve.GuestExecResult{}, fmt.Errorf("PVE returned 500: Agent error: PID lld does not exist")
		}
		return pve.GuestExecResult{ExitCode: 0}, nil
	})
	if err != nil || attempts != 2 {
		t.Fatal("bounded exact action replay failed", attempts, err)
	}
	attempts = 0
	result, err := replayWindowsHelperAction(t.Context(), action, func(context.Context, []byte) (pve.GuestExecResult, error) {
		attempts++
		return pve.GuestExecResult{ExitCode: 1}, nil
	})
	if err != nil || result.ExitCode != 1 || attempts != 1 {
		t.Fatal("authorization rejection was retried")
	}
	action.Target.InstanceID = ""
	_, err = replayWindowsHelperAction(t.Context(), action, func(context.Context, []byte) (pve.GuestExecResult, error) {
		t.Fatal("unbound action must never be submitted/replayed")
		return pve.GuestExecResult{}, nil
	})
	if err != ErrInvalid {
		t.Fatal("invalid action accepted")
	}
}

func windowsHelperPE(data []byte) bool {
	if len(data) < 128 || len(data) > 32*1024*1024 || string(data[:2]) != "MZ" {
		return false
	}
	offset := int(binary.LittleEndian.Uint32(data[60:64]))
	return offset >= 64 && offset <= len(data)-24 && string(data[offset:offset+4]) == "PE\x00\x00" && binary.LittleEndian.Uint16(data[offset+4:offset+6]) == 0x8664
}

func windowsTestGuestAddress(t *testing.T, client *pve.Client, machine pve.VM) string {
	t.Helper()
	interfaces, err := client.GuestNetworkInterfaces(t.Context(), machine.Node, machine.VMID)
	if err != nil {
		t.Fatal(err)
	}
	addresses := map[string]bool{}
	for _, iface := range interfaces {
		for _, item := range iface.IPAddresses {
			address, err := netip.ParseAddr(item.Address)
			if err == nil && address.Is4() && address.IsPrivate() {
				addresses[address.String()] = true
			}
		}
	}
	if len(addresses) != 1 {
		t.Fatal("acceptance VM must have one unambiguous private IPv4 address")
	}
	for address := range addresses {
		return address
	}
	panic("unreachable")
}

func serveWindowsTestArtifact(t *testing.T, bind netip.AddrPort, guest, marker, filename string, data []byte) string {
	t.Helper()
	if !bind.Addr().Is4() || !bind.Addr().IsPrivate() || (filename != "guest.exe" && filename != "session-tests.exe") {
		t.Fatal("explicit private IPv4 test artifact bind and fixed executable name required")
	}
	listener, err := net.Listen("tcp4", bind.String())
	if err != nil {
		t.Fatal(err)
	}
	path := "/" + marker + "/" + filename
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, WriteTimeout: time.Minute, IdleTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote, _, _ := net.SplitHostPort(r.RemoteAddr)
		if r.Method != http.MethodGet || r.URL.Path != path || r.URL.RawQuery != "" || remote != guest {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return "http://" + listener.Addr().String() + path
}

type windowsHelperLog struct {
	mu   sync.Mutex
	data []byte
}

func (log *windowsHelperLog) Write(data []byte) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.data = append(log.data, data[:min(len(data), max(0, 16384-len(log.data)))]...)
	return len(data), nil
}

func (log *windowsHelperLog) redacted(password string) string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return strings.ReplaceAll(string(log.data), password, "[redacted]")
}

func startWindowsHelperRDP(t *testing.T, marker string, index int, address, domain, username, password, fingerprint string) (func(), func()) {
	t.Helper()
	if executable := os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_SESSION_WORKER"); executable != "" {
		endpoint, err := netip.ParseAddrPort(address + ":3389")
		if err != nil {
			t.Fatal("invalid private RDP endpoint")
		}
		session, err := rdpsession.Start(t.Context(), rdpsession.Config{
			Executable: executable, Endpoint: endpoint, Username: username, Domain: domain,
			Password: password, CertificateSHA256: fingerprint, Width: 1280, Height: 720,
			ExpiresAt: time.Now().Add(20 * time.Minute),
		})
		if err != nil {
			t.Fatal("headless session worker did not reach a decoded frame", err)
		}
		t.Cleanup(session.Stop)
		width, height := session.Dimensions()
		t.Logf("headless native RDP worker connected with decoded %dx%d frame; no Docker/Xvfb", width, height)
		return session.Stop, func() {
			t.Helper()
			if err := session.Alive(); err != nil {
				t.Fatal("headless RDP transport ended", err)
			}
		}
	}
	name := fmt.Sprintf("vcw-helper-rdp-%s-%d", marker, index)
	args := []string{"run", "--rm", "--name", name, "--label", "vc-workspace.test-instance=" + marker,
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--tmpfs", "/tmp:rw,nosuid,nodev", "--tmpfs", "/run:rw,nosuid,nodev", "--tmpfs", "/root:rw,nosuid,nodev,mode=700",
		"-i", "vc-workspace-windows-session-test:local", "/args-from:stdin"}
	command := exec.Command("docker", args...)
	command.Stdin = strings.NewReader(strings.Join([]string{
		"/v:" + address + ":3389", "/u:" + username, "/d:" + domain, "/p:" + password,
		"/cert:fingerprint:sha256:" + fingerprint, "/size:1280x720", "-clipboard", "/log-level:ERROR",
	}, "\n") + "\n")
	log := &windowsHelperLog{}
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = command.Wait(); close(done) }()
	var once sync.Once
	stop := func() {
		t.Helper()
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			label, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "vc-workspace.test-instance"}}`, name).Output()
			if err == nil {
				if strings.TrimSpace(string(label)) != marker {
					t.Error("refuse stopping an unrelated RDP container")
					return
				}
				if err := exec.CommandContext(ctx, "docker", "stop", "-t", "3", name).Run(); err != nil {
					t.Error("stop fixture RDP container", err)
				}
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Error("RDP client failed to exit within cleanup deadline")
				_ = command.Process.Kill()
			}
			if exec.CommandContext(ctx, "docker", "inspect", name).Run() == nil {
				t.Error("fixture RDP container remains")
			}
		})
	}
	t.Cleanup(stop)
	running := func() {
		t.Helper()
		select {
		case <-done:
			t.Fatalf("RDP client exited before Helper discovery: %v\n%s", waitErr, log.redacted(password))
		default:
		}
	}
	return stop, running
}

func TestWindowsHelperFixturePEBounds(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("MZ"), make([]byte, 128)} {
		if windowsHelperPE(data) {
			t.Fatal("invalid executable accepted")
		}
	}
	data := make([]byte, 128)
	copy(data, "MZ")
	binary.LittleEndian.PutUint32(data[60:64], 64)
	copy(data[64:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(data[68:70], 0x8664)
	if !windowsHelperPE(data) {
		t.Fatal("bounded amd64 PE rejected")
	}
	binary.LittleEndian.PutUint32(data[60:64], 0xffffffff)
	if windowsHelperPE(data) {
		t.Fatal("out of bounds PE header accepted")
	}
}

func windowsHelperClockWithinBounds(before, after, guestNow int64) bool {
	return before > 0 && after >= before && after-before <= 6000 &&
		guestNow >= after-3000 && guestNow <= before+3000
}

func TestWindowsHelperClockBoundsIncludeTransportUncertainty(t *testing.T) {
	for _, sample := range []struct {
		before, after, guest int64
		allowed              bool
	}{
		{10000, 11000, 10500, true},
		{10000, 16000, 13000, true},
		{10000, 16001, 13000, false},
		{10000, 11000, 7999, false},
		{10000, 11000, 13001, false},
		{11000, 10000, 10500, false},
	} {
		if windowsHelperClockWithinBounds(sample.before, sample.after, sample.guest) != sample.allowed {
			t.Errorf("clock uncertainty accepted incorrectly: %+v", sample)
		}
	}
}

// Explicit recovery after a transport interruption during t.Cleanup. The
// remote script revalidates the protected marker, account journal and SID receipts
// rather than trusting an arbitrary path, username glob or registry prefix.
func TestLiveWindowsHelperFixtureRecovery(t *testing.T) {
	marker := os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_HELPER_CLEANUP_MARKER")
	if marker == "" {
		t.Skip("explicit failed fixture marker required")
	}
	if !regexp.MustCompile(`^[a-f0-9]{24}$`).MatchString(marker) {
		t.Fatal("invalid recovery marker")
	}
	vmid, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_COMPUTER_VMID"))
	if err != nil || vmid <= 0 {
		t.Fatal("explicit acceptance VMID required")
	}
	client := liveComputerClient(t)
	machine := liveComputerMachine(t, client, vmid)
	configuration, err := client.VMConfiguration(t.Context(), machine.Node, vmid)
	if err != nil || !strings.HasPrefix(configuration.OSType, "win") || machine.Status != "stopped" {
		t.Fatal("requires stopped managed Windows acceptance VM", err)
	}
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal("read recovery node headroom", err)
	}
	var node pve.Node
	for _, candidate := range summary.Nodes {
		if candidate.Name == machine.Node {
			node = candidate
		}
	}
	if err := nativeAcceptanceHeadroom(node, configuration.MemoryMB); err != nil {
		t.Fatal(err)
	}
	upid, err := client.ChangePowerState(t.Context(), machine.Node, vmid, "start")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		upid, err := client.ChangePowerState(ctx, machine.Node, vmid, "shutdown")
		if err != nil {
			t.Error("restore stopped Windows VM after recovery", err)
			return
		}
		waitLiveComputerTaskContext(t, ctx, client, machine.Node, upid)
		t.Logf("restored VM %d to stopped state", vmid)
	})
	waitLiveComputerTask(t, client, machine.Node, upid)
	waitLiveComputerQGA(t, client, machine)
	t.Logf("recovery VM %d: %d MiB RAM, %d cores, fixture %s", vmid, configuration.MemoryMB, configuration.Cores, marker)
	healthCtx, healthCancel := context.WithTimeout(t.Context(), 20*time.Second)
	health, healthErr := client.ExecGuest(healthCtx, machine.Node, vmid, windowsPowerShell(`$ErrorActionPreference='Stop'; $qga=Get-Process -Name 'qemu-ga' -ErrorAction SilentlyContinue; $events=@(Get-WinEvent -FilterHashtable @{LogName='System';ProviderName='Service Control Manager';Id=7031,7034;StartTime=(Get-Date).AddHours(-1)} -ErrorAction SilentlyContinue | Where-Object {$_.Message -match 'QEMU|qemu-ga'} | Select-Object -First 5 | ForEach-Object {@{event_id=$_.Id;created_utc=$_.TimeCreated.ToUniversalTime().ToString('o')}}); @{qga_process_count=@($qga).Count;qga_started_utc=@($qga | ForEach-Object {$_.StartTime.ToUniversalTime().ToString('o')});recent_qga_service_failures=$events} | ConvertTo-Json -Compress`))
	healthCancel()
	if healthErr == nil && health.ExitCode == 0 {
		t.Log("read-only QGA service diagnostics: " + strings.TrimSpace(health.Stdout))
	} else {
		t.Log("QGA service diagnostic unavailable; continuing exact cleanup")
	}
	script, err := windowsHelperFixtures.ReadFile("testdata/windows-helper/fixture.ps1")
	if err != nil {
		t.Fatal(err)
	}
	var expected []string
	if raw := os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_HELPER_CLEANUP_USERS"); raw != "" {
		expected = strings.Split(raw, ",")
		if len(expected) != 2 || expected[0] == expected[1] {
			t.Fatal("two distinct explicit fixture usernames required")
		}
		for _, username := range expected {
			if !regexp.MustCompile(`^vca[a-f0-9]{12}$`).MatchString(username) {
				t.Fatal("invalid fixture recovery username")
			}
		}
	}
	request := map[string]any{"operation": "inspect", "marker": marker}
	if len(expected) != 0 {
		request["expected_users"] = expected
	}
	input, _ := json.Marshal(request)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	result, err := client.ExecGuestWithInput(ctx, machine.Node, vmid, windowsPowerShell(string(script)), input)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("inspect exact fixture: exit=%d transport=%v stderr=%s", result.ExitCode, err, result.Stderr)
	}
	t.Log("read-only fixture inspection: " + strings.TrimSpace(result.Stdout))
	if strings.TrimSpace(result.Stdout) == "fixture-absent-and-no-owned-accounts-or-tasks" {
		return
	}
	var inspected struct {
		Marker string `json:"marker"`
		Users  []struct {
			Username string `json:"username"`
		} `json:"users"`
	}
	if json.Unmarshal([]byte(result.Stdout), &inspected) != nil || inspected.Marker != marker || len(inspected.Users) != 2 {
		t.Fatal("invalid fixture identity inspection")
	}
	if len(expected) == 2 && (expected[0] != inspected.Users[0].Username || expected[1] != inspected.Users[1].Username) {
		t.Fatal("protected fixture manifest does not match the explicit recovery identities")
	}
	expected = []string{inspected.Users[0].Username, inspected.Users[1].Username}
	request["operation"], request["expected_users"] = "cleanup", expected
	input, _ = json.Marshal(request)
	result, err = client.ExecGuestWithInput(ctx, machine.Node, vmid, windowsPowerShell(string(script)), input)
	if err != nil || result.ExitCode != 0 || !windowsHelperCleanupReceipt(result.Stdout) {
		t.Fatalf("recover exact fixture: exit=%d transport=%v stderr=%s", result.ExitCode, err, result.Stderr)
	}
	t.Log(strings.TrimSpace(result.Stdout))
	request["operation"] = "inspect"
	input, _ = json.Marshal(request)
	result, err = client.ExecGuestWithInput(ctx, machine.Node, vmid, windowsPowerShell(string(script)), input)
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != "fixture-absent-and-no-owned-accounts-or-tasks" {
		t.Fatal("recovery lacks independent absence proof", err, result.ExitCode, result.Stderr)
	}
}
