package computer

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

// Uploads a Windows Rust test executable into a fresh private staging directory.
// Does not replace the installed Guest agent or touch existing desktops. With
// the separate ACCOUNT_LIFECYCLE opt-in it creates/deletes random private local
// accounts (network password verification, no interactive login/profile) and
// verifies the SAM/registry baseline afterward.
// PROFILE_LIFECYCLE additionally opts in to fresh SID-bound Profile creation,
// DPAPI verification and exact Profile removal, with a ProfileList baseline.
// A previously stopped, explicitly selected acceptance VM is shut down afterward.
func TestLiveWindowsSessionPrimitives(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_SESSION_PRIMITIVES") != "true" {
		t.Skip("opt-in Windows native IPC/identity acceptance")
	}
	if os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_PROFILE_LIFECYCLE") == "true" && os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_ACCOUNT_LIFECYCLE") != "true" {
		t.Fatal("Profile acceptance also requires account lifecycle opt-in")
	}
	vmid, err := strconv.Atoi(os.Getenv("VC_WORKSPACE_LIVE_COMPUTER_VMID"))
	if err != nil || vmid <= 0 {
		t.Fatal("explicit positive acceptance VMID required")
	}
	path := os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_SESSION_TEST_BINARY")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("explicit local Windows test executable required", err)
	}
	if len(data) < 128 || len(data) > 32*1024*1024 || string(data[:2]) != "MZ" {
		t.Fatal("bounded Windows PE executable required")
	}
	offset := int(binary.LittleEndian.Uint32(data[60:64]))
	if offset < 64 || offset > len(data)-24 || string(data[offset:offset+4]) != "PE\x00\x00" || binary.LittleEndian.Uint16(data[offset+4:offset+6]) != 0x8664 {
		t.Fatal("Windows amd64 PE executable required")
	}
	installURL := ""
	if raw := os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_SESSION_INSTALL_URL"); raw != "" {
		installURL = validateLiveInstallURL(t, raw).String() + "session-tests.exe"
	}
	bind, bindErr := netip.ParseAddrPort(os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_SESSION_ARTIFACT_BIND"))
	if raw := os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_SESSION_ARTIFACT_BIND"); raw != "" &&
		(bindErr != nil || !bind.Addr().IsPrivate() || !bind.Addr().Is4() || installURL != "") {
		t.Fatal("use either an explicit private IPv4 artifact bind or an install URL")
	}
	client := liveComputerClient(t)
	machine := liveComputerMachine(t, client, vmid)
	configuration, err := client.VMConfiguration(t.Context(), machine.Node, vmid)
	if err != nil || !strings.HasPrefix(configuration.OSType, "win") {
		t.Fatal("Windows acceptance desktop required", err)
	}
	if machine.Status == "stopped" {
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
	} else if machine.Status != "running" {
		t.Fatal("acceptance VM must be stopped or running")
	}
	waitLiveComputerQGA(t, client, machine)
	// QGA represents Windows exception-style exits with signal and no exitcode.
	// This bounded process only exits with a chosen code; no fault/GUI/WER or
	// account change is needed to verify that absence never becomes success.
	for _, code := range []int{37, -1073741819} {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		result, err := client.ExecGuest(ctx, machine.Node, vmid, windowsPowerShell(fmt.Sprintf("[Environment]::Exit(%d)", code)))
		cancel()
		if code == 37 && (err != nil || result.ExitCode != code) {
			t.Fatal("normal nonzero Guest exit was not preserved", err, result.ExitCode)
		}
		if code < 0 && (err == nil || !strings.Contains(err.Error(), "signal/exception -1073741819") || result.ExitCode == 0) {
			t.Fatal("abnormal Windows exit was not explicitly rejected", err, result.ExitCode)
		}
	}
	t.Log("actual QGA normal/nonzero and Windows exception-only exits cannot become false success")
	exec := func(script string) string {
		t.Helper()
		result, err := client.ExecGuest(t.Context(), machine.Node, vmid, windowsPowerShell("$ErrorActionPreference='Stop'; "+script))
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("Windows test fixture: exit=%d transport=%v stderr=%s", result.ExitCode, err, result.Stderr)
		}
		return strings.TrimSpace(result.Stdout)
	}
	if exec("[Security.Principal.WindowsIdentity]::GetCurrent().User.Value") != "S-1-5-18" {
		t.Fatal("QGA must execute as LocalSystem")
	}
	if os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_ACCOUNT_LIFECYCLE") == "true" {
		const baselineScript = `$users=@(Get-LocalUser | Sort-Object Name | ForEach-Object { [ordered]@{name=$_.Name;sid=$_.SID.Value;enabled=$_.Enabled} }); $hive=[Microsoft.Win32.RegistryKey]::OpenBaseKey([Microsoft.Win32.RegistryHive]::LocalMachine,[Microsoft.Win32.RegistryView]::Registry64); $software=$hive.OpenSubKey('SOFTWARE'); $profileRoot=$software.OpenSubKey('Microsoft\Windows NT\CurrentVersion\ProfileList'); try { $keys=@($software.GetSubKeyNames() | Where-Object {$_ -like 'VCWorkspace.AgentAccountsV2*' -or $_ -like 'VCWorkspace.NativeAccountsV1*'} | Sort-Object); $profiles=@($profileRoot.GetSubKeyNames() | Sort-Object | ForEach-Object { $sid=$_; $p=$profileRoot.OpenSubKey($sid); try { [ordered]@{sid=$sid;path=[string]$p.GetValue('ProfileImagePath')} } finally { $p.Dispose() } }); [ordered]@{users=$users;keys=$keys;profiles=$profiles} | ConvertTo-Json -Depth 4 -Compress } finally {$profileRoot.Dispose();$software.Dispose();$hive.Dispose()}`
		readBaseline := func(ctx context.Context) (string, error) {
			return readWindowsNativeBaseline(ctx, func(probe context.Context) (pve.GuestExecResult, error) {
				return client.ExecGuest(probe, machine.Node, vmid, windowsPowerShell("$ErrorActionPreference='Stop'; "+baselineScript))
			})
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		baseline, err := readBaseline(ctx)
		cancel()
		if err != nil {
			t.Fatal("read initial native account baseline", err)
		}
		t.Logf("native SAM/ownership/ProfileList baseline SHA256: %x", sha256.Sum256([]byte(baseline)))
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			actual, err := readBaseline(ctx)
			if err != nil {
				t.Error("could not independently verify native account cleanup; retain for exact recovery", err)
			} else if actual != baseline {
				t.Errorf("native fixtures changed SAM/ownership/ProfileList baseline; observed SHA256: %x", sha256.Sum256([]byte(actual)))
			} else {
				t.Log("independent SAM/ownership/ProfileList cleanup baseline matches")
			}
		})
	}
	marker, err := auth.OpaqueToken(12)
	if err != nil {
		t.Fatal(err)
	}
	stage := `C:\ProgramData\vcw-session-test-` + marker
	if bind.IsValid() {
		installURL = serveWindowsTestArtifact(t, bind, windowsTestGuestAddress(t, client, machine), marker, "session-tests.exe", data)
	}
	t.Logf("private Windows test staging: %s", stage)
	// A fresh exact child of ProgramData, never an existing broad directory.
	// ACL is applied at creation; no interval of inherited permissive access.
	exec(fmt.Sprintf(`
$p='%s'
if (Test-Path -LiteralPath $p) { throw 'staging collision' }
$parent=Get-Item -LiteralPath 'C:\ProgramData' -Force
if ($parent.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'unsafe staging parent' }
$acl=New-Object Security.AccessControl.DirectorySecurity
$acl.SetSecurityDescriptorSddlForm('O:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)')
[IO.Directory]::CreateDirectory($p,$acl) | Out-Null
$created=Get-Item -LiteralPath $p
$actual=Get-Acl -LiteralPath $p
if (($created.Attributes -band [IO.FileAttributes]::ReparsePoint) -or $actual.GetOwner([Security.Principal.SecurityIdentifier]).Value -ne 'S-1-5-18' -or -not $actual.AreAccessRulesProtected) { throw 'unsafe staging ownership' }
$rules=@($actual.GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier]))
if ($rules.Count -ne 2) { throw 'unsafe staging ACL count' }
foreach($rule in $rules) { if ($rule.IdentityReference.Value -notin @('S-1-5-18','S-1-5-32-544') -or $rule.AccessControlType -ne 'Allow' -or $rule.FileSystemRights -ne 'FullControl') { throw 'unsafe staging ACL' } }
`, stage))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Exact allowlisted files only; refuse unsafe ownership, unknown files
		// or deleting an executable still in use by this native suite.
		script := windowsNativeCleanupScript(stage)
		result, err := client.ExecGuest(ctx, machine.Node, vmid, windowsPowerShell(script))
		if err != nil || result.ExitCode != 0 {
			t.Error("remove private Windows test files", stage, err, result.ExitCode, result.Stderr)
		}
	})
	if installURL != "" {
		// An opt-in private-IP artifact source reduces QGA round trips. No
		// redirect, unknown length or oversized response is accepted; the same
		// full-file SHA256/length gate below applies before execution.
		exec(fmt.Sprintf(`$ErrorActionPreference='Stop'; $request=[Net.HttpWebRequest]::Create('%s'); $request.Proxy=$null; $request.AllowAutoRedirect=$false; $request.Timeout=15000; $request.ReadWriteTimeout=10000; $clock=[Diagnostics.Stopwatch]::StartNew(); $response=$request.GetResponse(); try { if ([int]$response.StatusCode -ne 200 -or $response.ContentLength -ne %d) { throw 'unexpected artifact response' }; $artifactStream=$response.GetResponseStream(); $file=[IO.File]::Open('%s\session-tests.exe',[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None); try { $buffer=New-Object byte[] 65536; $total=0; while (($read=$artifactStream.Read($buffer,0,$buffer.Length)) -gt 0) { if ($clock.ElapsedMilliseconds -gt 45000) { throw 'artifact download timed out' }; $total+=$read; if ($total -gt %d) { throw 'artifact exceeds size limit' }; $file.Write($buffer,0,$read) }; if ($total -ne %d) { throw 'incomplete artifact' }; $file.Flush($true) } finally { $file.Dispose(); $artifactStream.Dispose() } } finally { $response.Dispose() }`, strings.ReplaceAll(installURL, "'", "''"), len(data), stage, len(data), len(data)))
	}
	for offset := 0; installURL == "" && offset < len(data); offset += 45 * 1024 {
		end := min(offset+45*1024, len(data))
		// Fixed-offset writes are idempotent even if QGA loses the result after
		// execution. Appending on retry would silently corrupt the test binary.
		chunkHash := fmt.Sprintf("%x", sha256.Sum256(data[offset:end]))
		script := fmt.Sprintf(`$bytes=[IO.File]::ReadAllBytes('%s\part'); $sha=[Security.Cryptography.SHA256]::Create(); try { $hash=[BitConverter]::ToString($sha.ComputeHash($bytes)).Replace('-','').ToLowerInvariant() } finally { $sha.Dispose() }; if ($bytes.Length -ne %d -or $hash -ne '%s') { throw ('chunk mismatch length='+$bytes.Length+' hash='+$hash) }; $f=[IO.File]::Open('%s\session-tests.exe',[IO.FileMode]::OpenOrCreate,[IO.FileAccess]::Write,[IO.FileShare]::None); try { $f.Position=%d; $f.Write($bytes,0,$bytes.Length); $f.SetLength(%d); $f.Flush($true) } finally { $f.Dispose() }`, stage, end-offset, chunkHash, stage, offset, end)
		if err := retryWindowsFixtureChunk(t.Context(), 60*time.Second, func(ctx context.Context) error {
			if err := client.WriteGuestBinaryFile(ctx, machine.Node, vmid, stage+`\part`, data[offset:end]); err != nil {
				return err
			}
			result, err := client.ExecGuest(ctx, machine.Node, vmid, windowsPowerShell(script))
			if err != nil {
				return err
			}
			if result.ExitCode != 0 {
				return fmt.Errorf("test staging chunk %d exited %d: %s", offset, result.ExitCode, result.Stderr)
			}
			return nil
		}); err != nil {
			t.Fatal("upload Windows test executable chunk", err)
		}
		if offset%(45*1024*32) == 0 {
			t.Logf("staged %d/%d Windows test bytes", end, len(data))
		}
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	actual := exec(fmt.Sprintf(`$p='%s\session-tests.exe'; ((Get-Item -LiteralPath $p).Length.ToString()+':'+(Get-FileHash -LiteralPath $p -Algorithm SHA256).Hash)`, stage))
	if !strings.EqualFold(actual, fmt.Sprintf("%d:%s", len(data), hash)) {
		t.Fatalf("Windows test executable hash/length mismatch: expected %d:%s got %s", len(data), hash, actual)
	}
	result := windowsNativeSuiteWithReceipt(t, client, machine, stage)
	if result.ExitCode != 0 {
		t.Fatalf("Windows native session tests failed: exit=%d\n%s\n%s", result.ExitCode, result.Stdout, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "test result: ok.") {
		t.Fatal("test executable did not run the native session suite")
	}
	for _, name := range []string{
		"platform::desktop::tests::desktop_object_names_are_bounded_and_borrowed",
		"platform::launcher::tests::launch_rejects_system_synthetic_and_invalid_logons",
		"platform::launcher::tests::pending_launch_rolls_back_but_commit_survives_launcher_handle_close",
		"platform::desktop::tests::input_desktop_never_admits_session_zero_or_invalid_handles",
		"platform::tests::authenticated_pipe_preserves_identity_and_round_trips_bytes",
		"platform::tests::pipe_process_pin_rejects_different_process_with_same_logon_identity",
		"platform::tests::persistent_listener_keeps_name_and_recovers_after_idle_timeout",
		"platform::tests::persistent_listener_recovers_after_rejected_and_stalled_clients",
		"platform::tests::authenticated_pipe_starts_a_separate_bounded_exchange_phase",
		"platform::worker::tests::worker_job_cleans_descendants_on_timeout_and_success_only",
		"platform::worker::tests::worker_job_kills_descendants_when_the_owner_process_exits",
		"platform::worker::tests::worker_cannot_create_a_breakaway_process",
		"platform::registry::accounts::native::secret::tests::system_secret_is_owner_bound_and_never_serializes_plaintext",
		"platform::registry::tests::registry_is_private_sid_bound_and_initialization_never_reactivates",
		"platform::registry::tests::registry_rejects_links_before_reading_or_writing_the_target",
		"platform::registry::tests::registry_rejects_wrong_sid_metadata_and_unsafe_acl_without_repairing_it",
		"platform::registry::tests::registry_kernel_acl_limits_reader_and_rejects_impersonated_updates",
		"platform::registry::tests::transactions::registry_transaction_publishes_all_users_and_fence_together",
		"platform::registry::tests::transactions::registry_transaction_drop_and_incomplete_stage_roll_back",
		"platform::registry::tests::transactions::registry_transaction_reserves_writer_before_reading_fence",
		"platform::registry::tests::transactions::registry_transaction_timeout_rolls_back_and_allows_recovery",
		"platform::registry::tests::transactions::registry_transaction_process_exit_rolls_back_without_rust_drop",
		"platform::registry::tests::transactions::registry_transaction_fence_stays_private_and_commit_rechecks_identity",
		"platform::registry::tests::transactions::registry_transaction_rejects_malformed_fence_without_resetting_it",
	} {
		if !strings.Contains(result.Stdout, "test "+name+" ... ok") {
			t.Fatalf("required native regression did not pass: %s", name)
		}
	}
	if os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_ACCOUNT_LIFECYCLE") == "true" {
		for _, name := range []string{
			"owned_account_lifecycle_retains_sid_and_never_reactivates_by_provision",
			"existing_unowned_account_is_never_adopted_disabled_or_changed",
			"interrupted_creation_recovers_only_matching_disabled_account",
			"deleted_or_replaced_owned_account_is_not_recreated_or_rebound",
			"account_gate_is_exclusive_and_released_after_handle_close",
			"unsafe_account_journal_is_rejected_without_acl_repair",
			"account_journal_is_kernel_private_even_to_its_account",
			"failed_bootstrap_cleanup_is_idempotent_without_creating_an_account",
			"helper_gate_requires_owned_enabled_account_and_serializes_disable",
			"expired_account_reconciliation_preserves_sid_and_rechecks_renewal_under_gate",
			"expiry_scan_does_not_adopt_unowned_or_replaced_accounts",
			"account_observation_retains_deleted_sid_and_reports_real_sam_expiry",
			"account_observation_never_adopts_unowned_or_unbound_namesakes",
			"lease_fence_rejects_late_mutations_and_retires_bootstrap_password",
			"lease_fence_crash_recovery_closes_uncommitted_sam_changes",
			"lease_fence_rejects_rebound_corrupt_and_concurrent_mutations",
			"lease_expiry_terminalizes_the_version_before_account_cleanup",
		} {
			if !strings.Contains(result.Stdout, "test platform::registry::accounts::tests::"+name+" ... ok") {
				t.Fatalf("required account lifecycle regression did not pass: %s", name)
			}
		}
		for _, name := range []string{
			"native_retirement_rotates_password_without_disabling_retained_account",
			"native_namespaces_do_not_adopt_agent_or_unowned_native_accounts",
			"native_gate_identity_and_corrupt_history_fail_closed",
			"native_deleted_sid_tombstone_never_rebinds_a_replacement",
			"native_local_expiry_terminalizes_without_resurrecting_old_credentials",
			"native_process_exit_recovers_pending_issue_and_retirement",
		} {
			if !strings.Contains(result.Stdout, "test platform::registry::accounts::native::tests::"+name+" ... ok") {
				t.Fatalf("required Native account lifecycle regression did not pass: %s", name)
			}
		}
		if os.Getenv("VC_WORKSPACE_LIVE_WINDOWS_PROFILE_LIFECYCLE") == "true" {
			for _, name := range []string{
				"denied_logons_password_change_preserves_profile_across_logons",
				"existing_profile_guard_preserves_data_and_allows_revoke_and_expiry",
				"expired_account_password_change_preserves_profile_across_logons",
				"password_change_and_key_migration_preserve_profile_across_logons",
			} {
				if !strings.Contains(result.Stdout, "test platform::registry::accounts::native::profile_tests::"+name+" ... ok") {
					t.Fatalf("required Profile/DPAPI regression did not pass: %s", name)
				}
			}
		}
	}
	t.Logf("VM %d: SHA256 %s\n%s", vmid, hash, result.Stdout)
}

// Admission check for this opt-in test, not a production capacity reservation.
// Keep at least 10% / 2 GiB for the host and other guests; Windows acceptance
// must be run serially per node because external starts can race this snapshot.
func nativeAcceptanceHeadroom(node pve.Node, memoryMB int) error {
	if node.Status != "online" || node.MemoryTotal <= 0 || node.MemoryUsed < 0 || node.MemoryUsed > node.MemoryTotal || memoryMB <= 0 || int64(memoryMB) > math.MaxInt64/(1<<20) {
		return fmt.Errorf("unknown acceptance node memory; refusing VM start")
	}
	reserve := max(int64(2<<30), node.MemoryTotal/10)
	available := node.MemoryTotal - node.MemoryUsed
	if available < reserve || int64(memoryMB)*(1<<20) > available-reserve {
		return fmt.Errorf("insufficient acceptance node memory: available=%d MiB, guest=%d MiB, reserve=%d MiB; run Windows tests serially", available/(1<<20), memoryMB, reserve/(1<<20))
	}
	return nil
}

func TestNativeAcceptanceRequiresHostMemoryHeadroom(t *testing.T) {
	good := pve.Node{Status: "online", MemoryTotal: 32 << 30, MemoryUsed: 16 << 30}
	if err := nativeAcceptanceHeadroom(good, 8192); err != nil {
		t.Fatal(err)
	}
	for _, node := range []pve.Node{{}, {Status: "offline", MemoryTotal: 32 << 30}, {Status: "online", MemoryTotal: 32 << 30, MemoryUsed: 25 << 30}, {Status: "online", MemoryTotal: 32 << 30, MemoryUsed: -1}, {Status: "online", MemoryTotal: 32 << 30, MemoryUsed: 33 << 30}} {
		if nativeAcceptanceHeadroom(node, 8192) == nil {
			t.Fatal("unsafe or unknown node budget accepted", node)
		}
	}
	for _, memory := range []int{0, -1, math.MaxInt} {
		if nativeAcceptanceHeadroom(good, memory) == nil {
			t.Fatal("invalid guest memory accepted")
		}
	}
}

// The fixed-offset, hash-checked test chunk is idempotent, including a lost QGA
// exec-status result. Do not generalize this retry to arbitrary Guest actions:
// a missing PID is an uncertain result, not evidence the command never ran.
func retryWindowsFixtureChunk(ctx context.Context, maximum time.Duration, operation func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, maximum)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := operation(ctx)
		if err == nil {
			return nil
		}
		message := strings.ToLower(err.Error())
		lostResult := strings.Contains(message, "pve returned 500") && strings.Contains(message, "agent error: pid ") && strings.Contains(message, "does not exist")
		if !transientGuestOperationError(err) && !lostResult {
			return err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func TestWindowsFixtureChunkRetriesOnlyIdempotentTransportLoss(t *testing.T) {
	attempts := 0
	err := retryWindowsFixtureChunk(t.Context(), time.Second, func(context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("PVE returned 500 Internal Server Error: Agent error: PID lld does not exist")
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("lost result retry: attempts=%d err=%v", attempts, err)
	}
	attempts = 0
	mismatch := errors.New("chunk mismatch")
	err = retryWindowsFixtureChunk(t.Context(), time.Second, func(context.Context) error {
		attempts++
		return mismatch
	})
	if !errors.Is(err, mismatch) || attempts != 1 {
		t.Fatal("integrity errors must not be retried")
	}
	started := time.Now()
	err = retryWindowsFixtureChunk(t.Context(), 20*time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatal("chunk attempt must inherit the overall retry deadline", err)
	}
}
