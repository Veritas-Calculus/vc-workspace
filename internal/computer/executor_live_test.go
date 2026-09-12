package computer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

// TestLiveComputerUseGuest is an explicitly opted-in destructive acceptance
// test. It may start a VM, upgrade the VC Workspace Agent when an install URL is
// supplied, operate the logged-in vdi desktop, and restore a previously stopped
// VM to its stopped state.
func TestLiveComputerUseGuest(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_COMPUTER_AUDIT") != "true" {
		t.Skip("set VC_WORKSPACE_LIVE_COMPUTER_AUDIT=true and VC_WORKSPACE_LIVE_COMPUTER_VMID to test a real interactive desktop")
	}
	vmid, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_COMPUTER_VMID")))
	if err != nil || vmid <= 0 {
		t.Fatal("VC_WORKSPACE_LIVE_COMPUTER_VMID must be a positive VMID")
	}
	client := liveComputerClient(t)
	machine := liveComputerMachine(t, client, vmid)
	wasStopped := machine.Status == "stopped"
	if wasStopped {
		upid, err := client.ChangePowerState(t.Context(), machine.Node, machine.VMID, "start")
		if err != nil {
			t.Fatal(err)
		}
		waitLiveComputerTask(t, client, machine.Node, upid)
		machine.Status = "running"
	}
	defer func() {
		if !wasStopped {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		upid, err := client.ChangePowerState(ctx, machine.Node, machine.VMID, "shutdown")
		if err != nil {
			t.Errorf("restore stopped VM state: %v", err)
			return
		}
		waitLiveComputerTaskContext(t, ctx, client, machine.Node, upid)
	}()

	configuration := waitLiveComputerQGA(t, client, machine)
	if localFile := strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_COMPUTER_INSTALL_FILE")); localFile != "" {
		guestSource := stageLiveComputerBinary(t, client, machine, configuration.OSType, localFile)
		installLiveComputerAgent(t, client, machine, configuration.OSType, nil, guestSource)
	} else if rawURL := strings.TrimSpace(os.Getenv("VC_WORKSPACE_LIVE_COMPUTER_INSTALL_URL")); rawURL != "" {
		baseURL := validateLiveInstallURL(t, rawURL)
		installLiveComputerAgent(t, client, machine, configuration.OSType, baseURL, "")
	}
	waitLiveHelper(t, client, machine, configuration.OSType, 45*time.Second)

	executor := NewPVEExecutor(client)
	leaseID := "lease_live_ai01_abcdefghijklmnopqrstuvwxyz"
	authority := Authority{
		SchemaVersion: SchemaVersion,
		LeaseID:       leaseID,
		ControlEpoch:  1,
		State:         "active",
		ExpiresUnixMS: time.Now().Add(2 * time.Minute).UnixMilli(),
	}
	if err := executor.ActivateAuthority(t.Context(), machine, authority); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.RevokeAuthority(context.Background(), machine, authority) }()

	screenshot := executeLiveAction(t, executor, machine, Request{
		Operation:  OperationScreenshot,
		Screenshot: &Screenshot{MaxWidth: 1280},
	}, leaseID)
	if screenshot.Screenshot == nil || screenshot.Screenshot.ContentType != "image/jpeg" || screenshot.Screenshot.Width < 320 {
		t.Fatalf("invalid screenshot response: %#v", screenshot.Screenshot)
	}
	image, err := base64.StdEncoding.DecodeString(screenshot.Screenshot.Data)
	if err != nil || len(image) < 1024 || image[0] != 0xff || image[1] != 0xd8 {
		t.Fatalf("invalid JPEG screenshot: bytes=%d error=%v", len(image), err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(image)) != screenshot.Screenshot.SHA256 {
		t.Fatal("screenshot digest mismatch")
	}
	linuxTerminalReady := false
	if !strings.HasPrefix(configuration.OSType, "win") {
		// A freshly upgraded legacy desktop may have started XFCE before its
		// AT-SPI packages were present. Launch a new GTK application in the
		// current session so the native provider has a real control tree to
		// enumerate, and reuse it for the input acceptance task below.
		executeLiveAction(t, executor, machine, Request{Operation: OperationKey, Key: &Key{Key: "t", Modifiers: []string{"control", "alt"}}}, leaseID)
		time.Sleep(time.Second)
		linuxTerminalReady = true
	}

	accessibility := executeLiveAction(t, executor, machine, Request{
		Operation: OperationAccessibility,
		Accessibility: &Accessibility{
			MaxDepth: 6,
			MaxNodes: 100,
		},
	}, leaseID)
	if accessibility.Accessibility == nil || accessibility.Accessibility.Source == "" || len(accessibility.Accessibility.Nodes) == 0 {
		t.Fatalf("interactive session returned no window snapshot: %#v", accessibility.Accessibility)
	}
	expectedAccessibilitySource := "linux_atspi"
	if strings.HasPrefix(configuration.OSType, "win") {
		expectedAccessibilitySource = "windows_uia"
	}
	if accessibility.Accessibility.Source != expectedAccessibilitySource {
		logLiveAccessibilityDiagnostics(t, client, machine, configuration.OSType)
		t.Fatalf("expected native accessibility source %q, got %q", expectedAccessibilitySource, accessibility.Accessibility.Source)
	}
	if strings.HasPrefix(configuration.OSType, "win") {
		runLiveWindowsTask(t, executor, client, machine, leaseID, accessibility.Accessibility)
	} else {
		runLiveLinuxTask(t, executor, client, machine, leaseID, linuxTerminalReady)
	}
	t.Logf("validated Computer Use on VMID %d: screenshot=%dx%d jpeg_bytes=%d windows=%d", machine.VMID, screenshot.Screenshot.Width, screenshot.Screenshot.Height, len(image), len(accessibility.Accessibility.Nodes))
}

func executeLiveAction(t *testing.T, executor *PVEExecutor, machine pve.VM, request Request, leaseID string) Response {
	t.Helper()
	started := time.Now()
	request.SchemaVersion = SchemaVersion
	request.RequestID = fmt.Sprintf("action_live_%020d", time.Now().UnixNano())
	request.LeaseID = leaseID
	request.ControlEpoch = 1
	request.ExpiresUnixMS = time.Now().Add(15 * time.Second).UnixMilli()
	request.ApplyDefaults()
	response, err := executor.Execute(t.Context(), machine, request, 15*time.Second)
	t.Logf("Computer Use %s action completed in %s", request.Operation, time.Since(started).Round(time.Millisecond))
	if err != nil {
		t.Fatalf("execute %s: %v", request.Operation, err)
	}
	return response
}

func runLiveLinuxTask(t *testing.T, executor *PVEExecutor, client *pve.Client, machine pve.VM, leaseID string, terminalReady bool) {
	const markerPath = "/tmp/vc-workspace-ai01.txt"
	_, _ = client.ExecGuest(t.Context(), machine.Node, machine.VMID, []string{"/bin/rm", "-f", markerPath})
	if !terminalReady {
		executeLiveAction(t, executor, machine, Request{Operation: OperationKey, Key: &Key{Key: "t", Modifiers: []string{"control", "alt"}}}, leaseID)
		time.Sleep(time.Second)
	}
	executeLiveAction(t, executor, machine, Request{Operation: OperationTypeText, Text: &Text{Value: "printf 'VC Workspace AI-01 Debian 13' > /tmp/vc-workspace-ai01.txt"}}, leaseID)
	executeLiveAction(t, executor, machine, Request{Operation: OperationKey, Key: &Key{Key: "enter"}}, leaseID)
	time.Sleep(time.Second)
	content, err := client.ReadGuestFile(t.Context(), machine.Node, machine.VMID, markerPath, 128)
	if err != nil || strings.TrimSpace(content) != "VC Workspace AI-01 Debian 13" {
		t.Fatalf("Linux interactive task did not create the expected marker: content=%q error=%v", content, err)
	}
	executeLiveAction(t, executor, machine, Request{Operation: OperationKey, Key: &Key{Key: "d", Modifiers: []string{"control"}}}, leaseID)
}

func runLiveWindowsTask(t *testing.T, executor *PVEExecutor, client *pve.Client, machine pve.VM, leaseID string, snapshot *AccessibilityResult) {
	const markerPath = `C:\Users\vdi\vc-workspace-ai01.txt`
	remove := `Remove-Item -LiteralPath 'C:\Users\vdi\vc-workspace-ai01.txt' -Force -ErrorAction SilentlyContinue`
	_, _ = client.ExecGuest(t.Context(), machine.Node, machine.VMID, windowsPowerShell(remove))
	x, y, ok := liveWindowsNeutralPoint(snapshot)
	if !ok {
		t.Fatal("Windows UIA snapshot did not expose a usable desktop area")
	}
	executeLiveAction(t, executor, machine, Request{Operation: OperationMouse, Mouse: &Mouse{Action: "click", X: x, Y: y, Button: "left"}}, leaseID)
	time.Sleep(time.Second)
	executeLiveAction(t, executor, machine, Request{Operation: OperationKey, Key: &Key{Key: "r", Modifiers: []string{"meta"}}}, leaseID)
	time.Sleep(time.Second)
	executeLiveAction(t, executor, machine, Request{Operation: OperationTypeText, Text: &Text{Value: "cmd.exe"}}, leaseID)
	executeLiveAction(t, executor, machine, Request{Operation: OperationKey, Key: &Key{Key: "enter"}}, leaseID)
	time.Sleep(2 * time.Second)
	command := `echo VC Workspace AI-01 Windows 11>C:\Users\vdi\vc-workspace-ai01.txt`
	executeLiveAction(t, executor, machine, Request{Operation: OperationTypeText, Text: &Text{Value: command, Sensitive: true}}, leaseID)
	executeLiveAction(t, executor, machine, Request{Operation: OperationKey, Key: &Key{Key: "enter"}}, leaseID)
	time.Sleep(2 * time.Second)
	contentResult, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, windowsPowerShell(`if (Test-Path -LiteralPath 'C:\Users\vdi\vc-workspace-ai01.txt') { Get-Content -Raw -LiteralPath 'C:\Users\vdi\vc-workspace-ai01.txt' } else { Write-Output '__missing__' }`))
	content := contentResult.Stdout
	if err != nil || contentResult.ExitCode != 0 || strings.TrimPrefix(strings.TrimSpace(content), "\ufeff") != "VC Workspace AI-01 Windows 11" {
		t.Fatalf("Windows interactive task did not save the expected marker: content=%q error=%v", content, err)
	}
}

func liveWindowsNeutralPoint(snapshot *AccessibilityResult) (int, int, bool) {
	if snapshot == nil {
		return 0, 0, false
	}
	right, bottom := 0, 0
	for _, node := range snapshot.Nodes {
		if node.X >= 0 && node.Y >= 0 {
			if edge := node.X + node.Width; edge > right {
				right = edge
			}
			if edge := node.Y + node.Height; edge > bottom {
				bottom = edge
			}
		}
	}
	if right < 320 || bottom < 240 {
		return 0, 0, false
	}
	return right / 2, bottom / 2, true
}

func installLiveComputerAgent(t *testing.T, client *pve.Client, machine pve.VM, osType string, baseURL *url.URL, guestSource string) {
	t.Helper()
	var command []string
	if strings.HasPrefix(osType, "win") {
		acquire := ""
		if guestSource != "" {
			acquire = fmt.Sprintf(`$new='%s';`, guestSource)
		} else {
			acquire = fmt.Sprintf(`$new=Join-Path $env:TEMP 'vc-workspace-agent-new.exe'; Invoke-WebRequest -UseBasicParsing -Uri '%s' -OutFile $new;`, baseURL.ResolveReference(&url.URL{Path: "vc-workspace-guest-agent-windows-amd64.exe"}).String())
		}
		script := fmt.Sprintf(`$ErrorActionPreference='Stop'; $agentDir='C:\Program Files\VC Workspace\Agent'; $stateDir='C:\ProgramData\VC Workspace\Agent'; $computerDir=Join-Path $stateDir 'computer'; %s Get-ScheduledTask -TaskName 'VC Workspace Guest Agent','VC Workspace Computer Helper' -ErrorAction SilentlyContinue | Stop-ScheduledTask -ErrorAction SilentlyContinue; Get-Process -Name 'vc-workspace-guest-agent' -ErrorAction SilentlyContinue | Stop-Process -Force; Start-Sleep -Milliseconds 500; New-Item -ItemType Directory -Path $agentDir,$stateDir,$computerDir,(Join-Path $computerDir 'requests'),(Join-Path $computerDir 'responses') -Force | Out-Null; Remove-Item -LiteralPath (Join-Path $computerDir 'helper-ready') -Force -ErrorAction SilentlyContinue; $vdiSID=(Get-LocalUser -Name 'vdi').SID.Value; & icacls.exe $computerDir /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' ('*' + $vdiSID + ':(OI)(CI)M') | Out-Null; if ($LASTEXITCODE -ne 0) { throw 'icacls failed' }; $agent=Join-Path $agentDir 'vc-workspace-guest-agent.exe'; Copy-Item $new $agent -Force; Remove-Item -LiteralPath $new -Force; & icacls.exe $agent /reset | Out-Null; if ($LASTEXITCODE -ne 0) { throw 'agent ACL reset failed' }; $taskSettings=New-ScheduledTaskSettingsSet -RestartCount 10 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit (New-TimeSpan -Days 3650); $agentAction=New-ScheduledTaskAction -Execute $agent -Argument ('--state-dir "{0}"' -f $stateDir); $agentTrigger=New-ScheduledTaskTrigger -AtStartup; $agentPrincipal=New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest; Register-ScheduledTask -TaskName 'VC Workspace Guest Agent' -Action $agentAction -Trigger $agentTrigger -Principal $agentPrincipal -Settings $taskSettings -Force | Out-Null; $helperAction=New-ScheduledTaskAction -Execute $agent -Argument ('computer-helper --state-dir "{0}"' -f $stateDir); $helperTrigger=New-ScheduledTaskTrigger -AtLogOn -User 'vdi'; $helperPrincipal=New-ScheduledTaskPrincipal -UserId ("{0}\vdi" -f $env:COMPUTERNAME) -LogonType Interactive -RunLevel Limited; Register-ScheduledTask -TaskName 'VC Workspace Computer Helper' -Action $helperAction -Trigger $helperTrigger -Principal $helperPrincipal -Settings $taskSettings -Force | Out-Null; Start-ScheduledTask -TaskName 'VC Workspace Guest Agent'; Start-ScheduledTask -TaskName 'VC Workspace Computer Helper' -ErrorAction SilentlyContinue; Write-Output 'installed'`, acquire)
		script = strings.Replace(script,
			`$helperAction=New-ScheduledTaskAction -Execute $agent -Argument ('computer-helper --state-dir "{0}"' -f $stateDir);`,
			`$helperLog=Join-Path $computerDir 'helper.log'; Remove-Item -LiteralPath $helperLog -Force -ErrorAction SilentlyContinue; $helperHost=Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'; $helperArgs='-NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -Command "& ''{0}'' computer-helper --state-dir ''{1}'' *>> ''{2}''"' -f $agent,$stateDir,$helperLog; $helperAction=New-ScheduledTaskAction -Execute $helperHost -Argument $helperArgs;`,
			1,
		)
		script = strings.Replace(script,
			`Start-ScheduledTask -TaskName 'VC Workspace Computer Helper' -ErrorAction SilentlyContinue; Write-Output 'installed'`,
			`Start-ScheduledTask -TaskName 'VC Workspace Computer Helper'; Start-Sleep -Milliseconds 750; if ((Get-ScheduledTask -TaskName 'VC Workspace Computer Helper').State -ne 'Running') { $detail=Get-Content -Raw -LiteralPath $helperLog -ErrorAction SilentlyContinue; throw ("Computer Helper failed to stay running: {0}" -f $detail) }; Write-Output 'installed'`,
			1,
		)
		script = strings.Replace(script,
			`-ExecutionTimeLimit (New-TimeSpan -Days 3650);`,
			`-ExecutionTimeLimit (New-TimeSpan -Days 3650) -MultipleInstances IgnoreNew;`,
			1,
		)
		command = windowsPowerShell(script)
	} else {
		acquire := ""
		if guestSource != "" {
			acquire = fmt.Sprintf(`mv '%s' /tmp/vc-workspace-agent-new;`, guestSource)
		} else {
			acquire = fmt.Sprintf(`curl --fail --silent --show-error '%s' -o /tmp/vc-workspace-agent-new;`, baseURL.ResolveReference(&url.URL{Path: "vc-workspace-guest-agent-linux-amd64"}).String())
		}
		script := fmt.Sprintf(`set -eu; %s export DEBIAN_FRONTEND=noninteractive; apt-get update; apt-get install -y --no-install-recommends at-spi2-core dbus-x11 libegl1 libgbm1 libpipewire-0.3-0 libwayland-client0 libwayland-server0 libxcb1; install -m 0755 /tmp/vc-workspace-agent-new /usr/local/sbin/vc-workspace-guest-agent; install -d -o root -g root -m 0755 /var/lib/vc-workspace; install -d -o vdi -g vdi -m 0700 /var/lib/vc-workspace/computer /var/lib/vc-workspace/computer/requests /var/lib/vc-workspace/computer/responses /home/vdi/.config/autostart; printf '%%s\n' '[Desktop Entry]' 'Type=Application' 'Name=VC Workspace Computer Helper' 'Exec=/usr/local/sbin/vc-workspace-guest-agent computer-helper --state-dir /var/lib/vc-workspace' 'OnlyShowIn=XFCE;' 'NoDisplay=true' 'X-GNOME-Autostart-enabled=true' > /home/vdi/.config/autostart/vc-workspace-computer-helper.desktop; chown vdi:vdi /home/vdi/.config/autostart/vc-workspace-computer-helper.desktop; chmod 0600 /home/vdi/.config/autostart/vc-workspace-computer-helper.desktop; printf '%%s\n' '[Unit]' 'Description=VC Workspace Guest Agent' 'After=network-online.target xrdp.service' 'Wants=network-online.target' '' '[Service]' 'Type=simple' 'ExecStart=/usr/local/sbin/vc-workspace-guest-agent --state-dir /var/lib/vc-workspace' 'Restart=always' 'RestartSec=3' 'NoNewPrivileges=true' 'ProtectSystem=strict' 'ProtectHome=true' 'PrivateTmp=true' 'ReadWritePaths=/var/lib/vc-workspace' '' '[Install]' 'WantedBy=multi-user.target' > /etc/systemd/system/vc-workspace-agent.service; systemctl daemon-reload; systemctl enable --now vc-workspace-agent; rm -f /var/lib/vc-workspace/computer/helper-ready /var/lib/vc-workspace/computer/helper-ready.tmp; session_pid=$(pgrep -u vdi -x xfce4-session | head -n1 || true); if [ -n "$session_pid" ]; then display=$(tr '\0' '\n' < "/proc/$session_pid/environ" | sed -n 's/^DISPLAY=//p' | head -n1); dbus=$(tr '\0' '\n' < "/proc/$session_pid/environ" | sed -n 's/^DBUS_SESSION_BUS_ADDRESS=//p' | head -n1); xauth=$(tr '\0' '\n' < "/proc/$session_pid/environ" | sed -n 's/^XAUTHORITY=//p' | head -n1); pkill -u vdi -f 'vc-workspace-guest-agent computer-helper' || true; runuser -u vdi -- env DISPLAY="$display" DBUS_SESSION_BUS_ADDRESS="$dbus" XAUTHORITY="$xauth" /usr/local/sbin/vc-workspace-guest-agent computer-helper --state-dir /var/lib/vc-workspace >/tmp/vc-workspace-computer-helper.log 2>&1 & fi; rm -f /tmp/vc-workspace-agent-new; echo installed`, acquire)
		command = []string{"/bin/sh", "-c", script}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	result, err := client.ExecGuest(ctx, machine.Node, machine.VMID, command)
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Stdout, "installed") {
		t.Fatalf("install Computer Use guest agent: exit=%d stdout=%q stderr=%q error=%v", result.ExitCode, result.Stdout, result.Stderr, err)
	}
}

func stageLiveComputerBinary(t *testing.T, client *pve.Client, machine pve.VM, osType, localPath string) string {
	t.Helper()
	payload, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read local Computer Use agent: %v", err)
	}
	if len(payload) == 0 || len(payload) > 32*1024*1024 {
		t.Fatalf("local Computer Use agent has an invalid size: %d", len(payload))
	}
	expectedHash := fmt.Sprintf("%x", sha256.Sum256(payload))
	guestTarget := "/var/lib/vc-workspace/computer-agent-upload"
	guestChunk := "/var/lib/vc-workspace/computer-agent-upload.part"
	prepare := []string{"/bin/sh", "-c", "install -d -m 0700 /var/lib/vc-workspace && : > /var/lib/vc-workspace/computer-agent-upload"}
	appendChunk := []string{"/bin/sh", "-c", "cat /var/lib/vc-workspace/computer-agent-upload.part >> /var/lib/vc-workspace/computer-agent-upload"}
	verify := []string{"/usr/bin/sha256sum", guestTarget}
	if strings.HasPrefix(osType, "win") {
		guestTarget = `C:\ProgramData\VCWorkspace\computer-agent-upload.exe`
		guestChunk = `C:\ProgramData\VCWorkspace\computer-agent-upload.part`
		prepare = windowsPowerShell(`New-Item -ItemType Directory -Path 'C:\ProgramData\VCWorkspace' -Force | Out-Null; [IO.File]::WriteAllBytes('C:\ProgramData\VCWorkspace\computer-agent-upload.exe',[byte[]]::new(0))`)
		appendChunk = windowsPowerShell(`$source='C:\ProgramData\VCWorkspace\computer-agent-upload.part'; $target='C:\ProgramData\VCWorkspace\computer-agent-upload.exe'; $bytes=[IO.File]::ReadAllBytes($source); $stream=[IO.File]::Open($target,[IO.FileMode]::Append,[IO.FileAccess]::Write,[IO.FileShare]::None); try { $stream.Write($bytes,0,$bytes.Length) } finally { $stream.Dispose() }`)
		verify = windowsPowerShell(`(Get-FileHash -Algorithm SHA256 -LiteralPath 'C:\ProgramData\VCWorkspace\computer-agent-upload.exe').Hash.ToLowerInvariant()`)
	}
	if result, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, prepare); err != nil || result.ExitCode != 0 {
		t.Fatalf("prepare guest staging file: exit=%d stderr=%q error=%v", result.ExitCode, result.Stderr, err)
	}
	const chunkSize = 45 * 1024
	for offset := 0; offset < len(payload); offset += chunkSize {
		end := min(offset+chunkSize, len(payload))
		if err := client.WriteGuestBinaryFile(t.Context(), machine.Node, machine.VMID, guestChunk, payload[offset:end]); err != nil {
			t.Fatalf("stage guest agent chunk at %d: %v", offset, err)
		}
		result, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, appendChunk)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("append guest agent chunk at %d: exit=%d stderr=%q error=%v", offset, result.ExitCode, result.Stderr, err)
		}
	}
	result, err := client.ExecGuest(t.Context(), machine.Node, machine.VMID, verify)
	if err != nil || result.ExitCode != 0 || !strings.Contains(strings.ToLower(result.Stdout), expectedHash) {
		t.Fatalf("verify staged guest agent: exit=%d stdout=%q stderr=%q error=%v", result.ExitCode, result.Stdout, result.Stderr, err)
	}
	t.Logf("staged %d-byte Computer Use agent on VMID %d with SHA-256 %s", len(payload), machine.VMID, expectedHash)
	return guestTarget
}

func validateLiveInstallURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		t.Fatal("VC_WORKSPACE_LIVE_COMPUTER_INSTALL_URL must be a plain HTTP URL on a private IP")
	}
	host := parsed.Hostname()
	address, err := netip.ParseAddr(host)
	if err != nil || !address.IsPrivate() {
		t.Fatal("VC_WORKSPACE_LIVE_COMPUTER_INSTALL_URL must use a private IP address")
	}
	if parsed.Port() == "" {
		t.Fatal("VC_WORKSPACE_LIVE_COMPUTER_INSTALL_URL must include an explicit port")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/"
	return parsed
}

func waitLiveHelper(t *testing.T, client *pve.Client, machine pve.VM, osType string, timeout time.Duration) {
	t.Helper()
	path := "/var/lib/vc-workspace/computer/helper-ready"
	if strings.HasPrefix(osType, "win") {
		deadline := time.Now().Add(timeout)
		command := windowsPowerShell(`$task=Get-ScheduledTask -TaskName 'VC Workspace Computer Helper' -ErrorAction SilentlyContinue; $helper=Get-Process -Name 'vc-workspace-guest-agent' -IncludeUserName -ErrorAction SilentlyContinue | Where-Object UserName -Match '\\vdi$'; if ($task.State -eq 'Running' -and $helper) { Write-Output 'helper-ready' } else { Write-Output 'helper-unavailable' }`)
		for time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			result, err := client.ExecGuest(ctx, machine.Node, machine.VMID, command)
			cancel()
			if err == nil && result.ExitCode == 0 && strings.Contains(result.Stdout, "helper-ready") {
				return
			}
			time.Sleep(time.Second)
		}
		logLiveHelperDiagnostics(t, client, machine, osType)
		t.Fatalf("interactive Computer Use helper is not ready on VMID %d; log in as vdi and retry", machine.VMID)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if value, err := client.ReadGuestFile(t.Context(), machine.Node, machine.VMID, path, 64); err == nil && liveHelperHeartbeatFresh(value, time.Now()) {
			return
		}
		time.Sleep(time.Second)
	}
	logLiveHelperDiagnostics(t, client, machine, osType)
	t.Fatalf("interactive Computer Use helper is not ready on VMID %d; log in as vdi and retry", machine.VMID)
}

func logLiveHelperDiagnostics(t *testing.T, client *pve.Client, machine pve.VM, osType string) {
	t.Helper()
	var command []string
	if strings.HasPrefix(osType, "win") {
		command = windowsPowerShell(`$task=Get-ScheduledTask -TaskName 'VC Workspace Computer Helper' -ErrorAction SilentlyContinue; $info=$task | Get-ScheduledTaskInfo -ErrorAction SilentlyContinue; $helper=Get-Process -Name 'vc-workspace-guest-agent' -IncludeUserName -ErrorAction SilentlyContinue | Where-Object UserName -Match '\\vdi$'; $log=Get-Content -Raw -LiteralPath 'C:\ProgramData\VC Workspace\Agent\computer\helper.log' -ErrorAction SilentlyContinue; [pscustomobject]@{helper_task_state=$task.State; helper_last_result=$info.LastTaskResult; helper_process=[bool]$helper; explorer_running=[bool](Get-Process explorer -IncludeUserName -ErrorAction SilentlyContinue | Where-Object UserName -Match '\\vdi$'); ready_marker=Test-Path 'C:\ProgramData\VC Workspace\Agent\computer\helper-ready'; helper_log=$log} | ConvertTo-Json -Compress`)
	} else {
		command = []string{"/bin/sh", "-c", `printf 'xrdp='; systemctl is-active xrdp 2>/dev/null || true; printf 'agent='; systemctl is-active vc-workspace-agent 2>/dev/null || true; printf 'sessions='; loginctl list-sessions --no-legend 2>/dev/null | awk '{print $1":"$3":"$5}' | paste -sd, -; printf '\nxfce_pid='; pgrep -u vdi -x xfce4-session | head -n1 || true; printf ' xorg_pid='; pgrep -u vdi -x Xorg | head -n1 || true; printf '\nspool='; stat -c '%U:%G:%a:%n' /var/lib/vc-workspace /var/lib/vc-workspace/computer /var/lib/vc-workspace/computer/requests /var/lib/vc-workspace/computer/responses /var/lib/vc-workspace/computer/helper-ready.tmp 2>/dev/null | paste -sd, -; printf '\nvdi_write='; runuser -u vdi -- sh -c 'test -w /var/lib/vc-workspace/computer && touch /var/lib/vc-workspace/computer/.write-test && rm /var/lib/vc-workspace/computer/.write-test' && printf ok || printf denied; printf '\nhelper_log='; tail -c 1024 /tmp/vc-workspace-computer-helper.log 2>/dev/null | tr '\n' ' ' || true; printf '\n'`}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	result, err := client.ExecGuest(ctx, machine.Node, machine.VMID, command)
	if err != nil {
		t.Logf("Computer Use helper diagnostics failed: %v", err)
		return
	}
	t.Logf("Computer Use helper diagnostics: exit=%d stdout=%q stderr=%q", result.ExitCode, result.Stdout, result.Stderr)
}

func logLiveAccessibilityDiagnostics(t *testing.T, client *pve.Client, machine pve.VM, osType string) {
	t.Helper()
	if strings.HasPrefix(osType, "win") {
		return
	}
	command := []string{"/bin/sh", "-c", `session_pid=$(pgrep -u vdi -x xfce4-session | head -n1 || true); helper_pid=$(pgrep -u vdi -f 'vc-workspace-guest-agent computer-helper' | head -n1 || true); printf 'session_dbus='; if [ -n "$session_pid" ] && tr '\0' '\n' < "/proc/$session_pid/environ" | grep -q '^DBUS_SESSION_BUS_ADDRESS='; then printf present; else printf missing; fi; printf ' helper_dbus='; if [ -n "$helper_pid" ] && tr '\0' '\n' < "/proc/$helper_pid/environ" | grep -q '^DBUS_SESSION_BUS_ADDRESS='; then printf present; else printf missing; fi; printf '\na11y_processes='; pgrep -a -u vdi -f 'at-spi|a11y' | cut -c1-240 | paste -sd, -; printf '\na11y_bus='; if [ -n "$helper_pid" ]; then dbus=$(tr '\0' '\n' < "/proc/$helper_pid/environ" | sed -n 's/^DBUS_SESSION_BUS_ADDRESS=//p' | head -n1); runuser -u vdi -- env DBUS_SESSION_BUS_ADDRESS="$dbus" dbus-send --session --print-reply --reply-timeout=3000 --dest=org.a11y.Bus /org/a11y/bus org.a11y.Bus.GetAddress 2>&1 | tr '\n' ' ' | cut -c1-480; fi; printf '\n'`}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	result, err := client.ExecGuest(ctx, machine.Node, machine.VMID, command)
	if err != nil {
		t.Logf("native accessibility diagnostics failed: %v", err)
		return
	}
	t.Logf("native accessibility diagnostics: exit=%d stdout=%q stderr=%q", result.ExitCode, result.Stdout, result.Stderr)
}

func liveHelperHeartbeatFresh(value string, now time.Time) bool {
	timestamp, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return false
	}
	age := now.UnixMilli() - timestamp
	return age >= -5_000 && age <= 15_000
}

func waitLiveComputerQGA(t *testing.T, client *pve.Client, machine pve.VM) pve.VMConfiguration {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		configuration, err := client.VMConfiguration(t.Context(), machine.Node, machine.VMID)
		if err == nil {
			command := []string{"/bin/true"}
			if strings.HasPrefix(configuration.OSType, "win") {
				command = windowsPowerShell(`Write-Output 'qga-ready'`)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			result, execErr := client.ExecGuest(ctx, machine.Node, machine.VMID, command)
			cancel()
			if execErr == nil && result.ExitCode == 0 {
				return configuration
			} else {
				lastErr = execErr
				if lastErr == nil {
					lastErr = fmt.Errorf("guest readiness command exited %d", result.ExitCode)
				}
			}
		} else {
			lastErr = err
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("QGA/readiness did not become available: %v", lastErr)
	return pve.VMConfiguration{}
}

func liveComputerMachine(t *testing.T, client *pve.Client, vmid int) pve.VM {
	t.Helper()
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, machine := range summary.VMs {
		if machine.VMID == vmid {
			if machine.Kind != "qemu" || machine.Template ||
				(!containsLiveString(machine.Tags, "vc-workspace") && !containsLiveString(machine.Tags, "vc-vdi")) {
				t.Fatalf("VMID %d is not a tagged non-template VC Workspace QEMU desktop", vmid)
			}
			return machine
		}
	}
	t.Fatalf("VMID %d was not found", vmid)
	return pve.VM{}
}

func containsLiveString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func liveComputerClient(t *testing.T) *pve.Client {
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

func waitLiveComputerTask(t *testing.T, client *pve.Client, node, upid string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	waitLiveComputerTaskContext(t, ctx, client, node, upid)
}

func waitLiveComputerTaskContext(t *testing.T, ctx context.Context, client *pve.Client, node, upid string) {
	t.Helper()
	for {
		status, err := client.TaskStatus(ctx, node, upid)
		if err != nil {
			t.Fatal(err)
		}
		if status.Status == "stopped" {
			if status.ExitStatus != "OK" {
				t.Fatalf("PVE task failed: %s", status.ExitStatus)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func windowsPowerShell(script string) []string {
	return []string{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}
}
