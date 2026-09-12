package computer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

var nativeFixtureMarker = regexp.MustCompile(`^[A-Za-z0-9_-]{16}$`)

// Only the exact private directory allocated for this native test is accepted.
func windowsNativeStageGuard(stage string) string {
	marker := strings.TrimPrefix(stage, `C:\ProgramData\vcw-session-test-`)
	if !nativeFixtureMarker.MatchString(marker) || stage != `C:\ProgramData\vcw-session-test-`+marker {
		panic("invalid native fixture directory")
	}
	return fmt.Sprintf(`$ErrorActionPreference='Stop'; $p='%s'
if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne 'S-1-5-18') { throw 'SYSTEM required' }
if (-not (Test-Path -LiteralPath $p)) { throw 'fixture absent' }
$item=Get-Item -LiteralPath $p -Force
$acl=Get-Acl -LiteralPath $p
$rules=@($acl.GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier]))
if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -or -not $item.PSIsContainer -or $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value -ne 'S-1-5-18' -or -not $acl.AreAccessRulesProtected -or $rules.Count -ne 2) { throw 'unsafe fixture directory' }
foreach($rule in $rules) { if ($rule.IdentityReference.Value -notin @('S-1-5-18','S-1-5-32-544') -or $rule.AccessControlType -ne 'Allow' -or $rule.FileSystemRights -ne 'FullControl') { throw 'unsafe fixture ACL' } }
foreach($item in @(Get-ChildItem -LiteralPath $p -Force)) { if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -or $item.Name -cnotin @('part','session-tests.exe','started','output.log','exit.tmp','exit')) { throw 'unexpected fixture file' } }
`, stage)
}

// Keep the result in the SYSTEM-private stage. Lost QGA exec-status does not
// require rerunning the suite or deleting its still-open executable.
func windowsNativeSuiteWithReceipt(t *testing.T, client *pve.Client, machine pve.VM, stage string) pve.GuestExecResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	// Exactly one invocation; CreateNew rejects accidental duplicate launches.
	script := windowsNativeStageGuard(stage) + `
$started=[IO.File]::Open((Join-Path $p 'started'),[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None); $started.Dispose()
$ErrorActionPreference='Continue'
& (Join-Path $p 'session-tests.exe') --test-threads=1 --nocapture 2>&1 | Out-File -LiteralPath (Join-Path $p 'output.log') -Encoding utf8
$code=$LASTEXITCODE
`
	if os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_ACCOUNT_LIFECYCLE") == "true" {
		// Explicit opt-in selects only these destructive tests; other ignored
		// tests deliberately exit/kill processes and must never be swept in.
		script += `
if ($code -eq 0) {
  $env:VC_WORKSPACE_NATIVE_ACCOUNT_FIXTURE='isolated'
  & (Join-Path $p 'session-tests.exe') --ignored 'platform::registry::accounts::tests::' --test-threads=1 --show-output 2>&1 | Out-File -LiteralPath (Join-Path $p 'output.log') -Encoding utf8 -Append
  $code=$LASTEXITCODE
}
if ($code -eq 0) {
  & (Join-Path $p 'session-tests.exe') --ignored 'platform::registry::accounts::native::tests::' --test-threads=1 --show-output 2>&1 | Out-File -LiteralPath (Join-Path $p 'output.log') -Encoding utf8 -Append
  $code=$LASTEXITCODE
}
`
		if os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_PROFILE_LIFECYCLE") == "true" {
			script += `
if ($code -eq 0) {
  $env:VC_WORKSPACE_NATIVE_PROFILE_FIXTURE='isolated'
  & (Join-Path $p 'session-tests.exe') --ignored 'platform::registry::accounts::native::profile_tests::' --test-threads=1 --show-output 2>&1 | Out-File -LiteralPath (Join-Path $p 'output.log') -Encoding utf8 -Append
  $code=$LASTEXITCODE
}
`
		}
	}
	script += `
$ErrorActionPreference='Stop'
if ($null -eq $code) { throw 'native process did not report an exit code' }
[IO.File]::WriteAllText((Join-Path $p 'exit.tmp'),$code.ToString())
[IO.File]::Move((Join-Path $p 'exit.tmp'),(Join-Path $p 'exit'))
`
	_, startErr := client.ExecGuest(ctx, machine.Node, machine.VMID, windowsPowerShell(script))
	if startErr != nil {
		t.Log("native launch result uncertain; read the same private receipt without rerunning the suite")
	}
	for ctx.Err() == nil {
		probe, stop := context.WithTimeout(ctx, 30*time.Second)
		result, err := client.ExecGuest(probe, machine.Node, machine.VMID, windowsPowerShell(windowsNativeStageGuard(stage)+`
$receipt=Join-Path $p 'exit'
if (-not (Test-Path -LiteralPath $receipt)) { '{"ready":false}'; exit 0 }
if ((Get-Item -LiteralPath $receipt).Length -gt 32) { throw 'invalid native receipt size' }
$code=[IO.File]::ReadAllText($receipt)
if ($code -notmatch '^-?[0-9]{1,10}$') { throw 'invalid native exit code' }
$log=Join-Path $p 'output.log'
if ((Get-Item -LiteralPath $log).Length -gt 524288) { throw 'native output exceeds limit' }
@{ready=$true;code=[long]$code;output=[Convert]::ToBase64String([IO.File]::ReadAllBytes($log))} | ConvertTo-Json -Compress
`))
		stop()
		if err == nil && result.ExitCode == 0 {
			receipt, err := parseWindowsNativeReceipt(result.Stdout)
			if err != nil {
				t.Fatal(err)
			}
			if receipt != nil {
				return *receipt
			}
		} else if err == nil || !nativeReceiptReadUncertain(err) {
			t.Fatal("read native test receipt", err, result.ExitCode, result.Stderr)
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	t.Fatal("native test receipt unavailable before deadline; suite was not rerun", startErr)
	return pve.GuestExecResult{}
}

// An observation timeout says nothing about the already-launched suite. Only
// this read-only receipt probe may retry it, within the original outer budget;
// never apply this classification to setup, account mutations or cleanup.
func nativeReceiptReadUncertain(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || windowsTestQGAResultUncertain(err)
}

// This retries only a read-only SAM/registry snapshot, never the native suite
// or a mutation. A lost QGA PID is not evidence that account state changed.
// A definite read/parse failure, or a successful but different snapshot, is not
// retried until it happens to match; the caller compares the single result.
func readWindowsNativeBaseline(ctx context.Context, read func(context.Context) (pve.GuestExecResult, error)) (string, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		probe, cancel := context.WithTimeout(ctx, 9*time.Second)
		result, err := read(probe)
		cancel()
		if err == nil {
			if result.ExitCode != 0 {
				return "", fmt.Errorf("native baseline command exited %d", result.ExitCode)
			}
			raw := strings.TrimSpace(result.Stdout)
			var snapshot struct {
				Users []struct {
					Name    string `json:"name"`
					SID     string `json:"sid"`
					Enabled *bool  `json:"enabled"`
				} `json:"users"`
				Keys     []string `json:"keys"`
				Profiles []struct {
					SID  string `json:"sid"`
					Path string `json:"path"`
				} `json:"profiles"`
			}
			decoder := json.NewDecoder(strings.NewReader(raw))
			decoder.DisallowUnknownFields()
			if len(raw) == 0 || len(raw) > 128*1024 || !json.Valid([]byte(raw)) || decoder.Decode(&snapshot) != nil || snapshot.Users == nil || snapshot.Keys == nil || snapshot.Profiles == nil {
				return "", fmt.Errorf("invalid native baseline JSON")
			}
			for _, user := range snapshot.Users {
				if user.Name == "" || user.SID == "" || user.Enabled == nil {
					return "", fmt.Errorf("incomplete native account baseline")
				}
			}
			for _, profile := range snapshot.Profiles {
				if profile.SID == "" || profile.Path == "" {
					return "", fmt.Errorf("incomplete native profile baseline")
				}
			}
			return raw, nil
		}
		last = err
		if !nativeReceiptReadUncertain(err) || attempt == 2 {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return "", last
}

func TestNativeBaselineReadRetriesOnlyUncertainObservations(t *testing.T) {
	for _, uncertain := range []error{context.DeadlineExceeded, errors.New("PVE returned 500: Agent error: PID lld does not exist")} {
		calls := 0
		got, err := readWindowsNativeBaseline(t.Context(), func(ctx context.Context) (pve.GuestExecResult, error) {
			calls++
			if _, bounded := ctx.Deadline(); !bounded {
				t.Fatal("baseline probe has no deadline")
			}
			if calls == 1 {
				return pve.GuestExecResult{}, uncertain
			}
			return pve.GuestExecResult{Stdout: `{"users":[],"keys":[],"profiles":[]}`}, nil
		})
		if err != nil || calls != 2 || got != `{"users":[],"keys":[],"profiles":[]}` {
			t.Fatal("read-only baseline did not recover", calls, got, err)
		}
	}
	for _, tc := range []struct {
		result pve.GuestExecResult
		err    error
	}{
		{result: pve.GuestExecResult{ExitCode: 37}},
		{result: pve.GuestExecResult{Stdout: "incomplete"}},
		{result: pve.GuestExecResult{Stdout: `null`}},
		{result: pve.GuestExecResult{Stdout: `{"users":null,"keys":[],"profiles":[]}`}},
		{result: pve.GuestExecResult{Stdout: `{"users":[],"keys":[],"profiles":[],"extra":true}`}},
		{result: pve.GuestExecResult{Stdout: `{"users":[{"name":"test","sid":"S-1-5-21-1-2-3-1001"}],"keys":[],"profiles":[]}`}},
		{result: pve.GuestExecResult{Stdout: `{"users":[],"keys":[]}`}},
		{result: pve.GuestExecResult{Stdout: `{"users":[],"keys":[],"profiles":null}`}},
		{result: pve.GuestExecResult{Stdout: `{"users":[],"keys":[],"profiles":[{"sid":"S-1-5-21-1-2-3-1001"}]}`}},
		{result: pve.GuestExecResult{Stdout: `{"users":[],"keys":[],"profiles":[{"sid":"","path":"C:\\Users\\test"}]}`}},
		{result: pve.GuestExecResult{Stdout: `{"users":[],"keys":[],"profiles":[{"sid":"S-1-5-21-1-2-3-1001","path":false}]}`}},
		{result: pve.GuestExecResult{Stdout: `{"users":[],"keys":[],"profiles":[{"sid":"S-1-5-21-1-2-3-1001","path":"C:\\Users\\test","extra":true}]}`}},
		{err: errors.New("permission denied")},
	} {
		calls := 0
		_, err := readWindowsNativeBaseline(t.Context(), func(context.Context) (pve.GuestExecResult, error) {
			calls++
			return tc.result, tc.err
		})
		if err == nil || calls != 1 {
			t.Fatal("definite baseline failure retried", calls, err)
		}
	}
	calls := 0
	_, err := readWindowsNativeBaseline(t.Context(), func(context.Context) (pve.GuestExecResult, error) {
		calls++
		return pve.GuestExecResult{}, context.DeadlineExceeded
	})
	if !errors.Is(err, context.DeadlineExceeded) || calls != 3 {
		t.Fatal("unbounded baseline retries", calls, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = readWindowsNativeBaseline(ctx, func(context.Context) (pve.GuestExecResult, error) {
		t.Fatal("canceled baseline executed a command")
		return pve.GuestExecResult{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestNativeReceiptReadTimeoutDoesNotMeanSuiteFailed(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, fmt.Errorf("probe: %w", context.DeadlineExceeded), errors.New("PVE returned 500: Agent error: PID 44 does not exist")} {
		if !nativeReceiptReadUncertain(err) {
			t.Fatalf("uncertain observation classified as terminal: %v", err)
		}
	}
	for _, err := range []error{nil, context.Canceled, errors.New("permission denied"), errors.New("invalid bounded native receipt")} {
		if nativeReceiptReadUncertain(err) {
			t.Fatalf("definite failure classified as retryable: %v", err)
		}
	}
}

func parseWindowsNativeReceipt(raw string) (*pve.GuestExecResult, error) {
	var receipt struct {
		Ready *bool   `json:"ready"`
		Code  *int64  `json:"code"`
		Log   *string `json:"output"`
	}
	if len(raw) > 768*1024 || !json.Valid([]byte(raw)) {
		return nil, fmt.Errorf("invalid bounded native receipt")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || receipt.Ready == nil {
		return nil, fmt.Errorf("invalid native receipt schema")
	}
	if !*receipt.Ready {
		if receipt.Code != nil || receipt.Log != nil {
			return nil, fmt.Errorf("incomplete native receipt has a result")
		}
		return nil, nil
	}
	if receipt.Code == nil || receipt.Log == nil || *receipt.Code < -2147483648 || *receipt.Code > 4294967295 {
		return nil, fmt.Errorf("native receipt missing exit code or output")
	}
	output, err := base64.StdEncoding.Strict().DecodeString(*receipt.Log)
	if err != nil || len(output) == 0 || len(output) > 524288 || !utf8.Valid(output) {
		return nil, fmt.Errorf("invalid bounded native output")
	}
	return &pve.GuestExecResult{ExitCode: int(*receipt.Code), Stdout: strings.TrimPrefix(string(output), "\ufeff")}, nil
}

func windowsNativeCleanupScript(stage string) string {
	return windowsNativeStageGuard(stage) + `
$exe=Join-Path $p 'session-tests.exe'
if (@(Get-CimInstance Win32_Process -Filter "Name='session-tests.exe'" | Where-Object {$_.ExecutablePath -eq $exe}).Count -gt 0) { throw 'native fixture still running' }
foreach($name in @('part','session-tests.exe','started','output.log','exit.tmp','exit')) { $file=Join-Path $p $name; if(Test-Path -LiteralPath $file) { Remove-Item -LiteralPath $file -Force } }
[IO.Directory]::Delete($p,$false)
`
}

// Explicit recovery after the VM has stopped. Never executes the artifact;
// validates its recorded hash before deleting exact test-owned files.
func TestLiveWindowsNativeFixtureRecovery(t *testing.T) {
	marker := os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_SESSION_CLEANUP_MARKER")
	if marker == "" {
		t.Skip("explicit native fixture marker required")
	}
	hash := os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_SESSION_CLEANUP_SHA256")
	if !nativeFixtureMarker.MatchString(marker) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(hash) {
		t.Fatal("exact native fixture marker and artifact SHA256 required")
	}
	vmid, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_COMPUTER_VMID"))
	if err != nil || vmid <= 0 {
		t.Fatal("explicit VMID required")
	}
	client := liveComputerClient(t)
	machine := liveComputerMachine(t, client, vmid)
	config, err := client.VMConfiguration(t.Context(), machine.Node, vmid)
	if err != nil || !strings.HasPrefix(config.OSType, "win") || machine.Status != "stopped" {
		t.Fatal("requires stopped managed Windows acceptance VM", err)
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
			t.Error("restore stopped native acceptance VM", err)
			return
		}
		waitLiveComputerTaskContext(t, ctx, client, machine.Node, upid)
		t.Log("restored Windows native acceptance VM to stopped")
	})
	waitLiveComputerTask(t, client, machine.Node, upid)
	waitLiveComputerQGA(t, client, machine)
	stage := `C:\ProgramData\vcw-session-test-` + marker
	script := windowsNativeStageGuard(stage) + fmt.Sprintf(`
if ((Get-FileHash -LiteralPath (Join-Path $p 'session-tests.exe') -Algorithm SHA256).Hash.ToLowerInvariant() -ne '%s') { throw 'artifact ownership mismatch' }
# Inspect only. Unknown registry test keys are never deleted by a prefix sweep.
$hive=[Microsoft.Win32.RegistryKey]::OpenBaseKey([Microsoft.Win32.RegistryHive]::LocalMachine,[Microsoft.Win32.RegistryView]::Registry64)
$software=$hive.OpenSubKey('SOFTWARE')
try { @($software.GetSubKeyNames() | Where-Object {$_ -match '^VCWorkspace\.ComputerV2\.Test\.[a-f0-9]{32}$'}) | ConvertTo-Json -Compress } finally { $software.Dispose(); $hive.Dispose() }
`, hash)
	result, err := client.ExecGuest(t.Context(), machine.Node, vmid, windowsPowerShell(script))
	if err != nil || result.ExitCode != 0 {
		t.Fatal("inspect exact failed native fixture", err, result.ExitCode, result.Stderr)
	}
	if strings.TrimSpace(result.Stdout) != "" {
		t.Log("registry test names retained for separate ownership inspection: " + result.Stdout)
	} else {
		t.Log("no native registry test keys remain")
	}
	result, err = client.ExecGuest(t.Context(), machine.Node, vmid, windowsPowerShell(windowsNativeCleanupScript(stage)))
	if err != nil || result.ExitCode != 0 {
		t.Fatal("clean exact failed native fixture", err, result.ExitCode, result.Stderr)
	}
	t.Log("removed exact failed native staging directory: " + stage)
}

func TestWindowsNativeStageGuardRejectsBroadOrInjectedPaths(t *testing.T) {
	for _, path := range []string{`C:\ProgramData`, `C:\ProgramData\vcw-session-test-..`, `C:\ProgramData\vcw-session-test-'injection`, `C:\other\abcdefghijklmnop`} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("unsafe stage accepted: %q", path)
				}
			}()
			windowsNativeStageGuard(path)
		}()
	}
	if script := windowsNativeStageGuard(`C:\ProgramData\vcw-session-test-abcdefghijklmnop`); !strings.Contains(script, "unsafe fixture ACL") {
		t.Fatal("missing private directory validation")
	}
}

func TestWindowsNativeReceiptRequiresExplicitBoundedResult(t *testing.T) {
	if value, err := parseWindowsNativeReceipt(`{"ready":false}`); err != nil || value != nil {
		t.Fatal(value, err)
	}
	for _, raw := range []string{`{}`, `null`, `{"ready":true}`, `{"ready":true,"output":"b2s="}`, `{"ready":true,"code":0}`, `{"ready":false,"code":0}`, `{"ready":true,"code":0,"output":"%%%"}`, `{"ready":true,"code":4294967296,"output":"b2s="}`, `{"ready":true,"code":0,"output":"b2s=","unknown":1}`, `{"ready":false} {"ready":false}`, strings.Repeat(" ", 768*1024+1)} {
		if _, err := parseWindowsNativeReceipt(raw); err == nil {
			t.Fatalf("invalid receipt accepted: %.120q", raw)
		}
	}
	for _, code := range []int{0, 101, -1} {
		raw, _ := json.Marshal(map[string]any{"ready": true, "code": code, "output": base64.StdEncoding.EncodeToString([]byte("\ufefftest result 中文"))})
		value, err := parseWindowsNativeReceipt(string(raw))
		if err != nil || value.ExitCode != code || value.Stdout != "test result 中文" {
			t.Fatal(value, err)
		}
	}
}
