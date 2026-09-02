$ErrorActionPreference = 'Stop'

$cdromRoots = Get-CimInstance Win32_LogicalDisk -Filter 'DriveType = 5' | ForEach-Object { "$($_.DeviceID)\" }
$virtioGuestToolsMsi = $cdromRoots | ForEach-Object {
    $candidate = Join-Path $_ 'virtio-win-gt-x64.msi'
    if (Test-Path $candidate) { Get-Item $candidate }
} | Select-Object -First 1
if (-not $virtioGuestToolsMsi) {
    throw 'VirtIO guest tools MSI was not found on a CD-ROM drive'
}

# Packer asks PVE for the guest IP through QEMU-GA. Install the complete
# guest-tools bundle first so the VirtIO Serial driver exists before the
# agent service starts; installing only qemu-ga leaves that channel unusable.
$virtioGuestToolsInstall = Start-Process msiexec.exe -ArgumentList '/i', $virtioGuestToolsMsi.FullName, '/qn', '/norestart' -Wait -PassThru
if ($virtioGuestToolsInstall.ExitCode -notin 0, 3010) {
    throw "VirtIO guest tools installation failed with exit code $($virtioGuestToolsInstall.ExitCode)"
}

if (-not (Get-Service QEMU-GA -ErrorAction SilentlyContinue)) {
    $qemuGuestAgentMsi = $cdromRoots | ForEach-Object {
        Get-ChildItem -Path $_ -Filter 'qemu-ga-x86_64.msi' -Recurse -ErrorAction SilentlyContinue | Select-Object -First 1
    } | Select-Object -First 1
    if (-not $qemuGuestAgentMsi) {
        throw 'QEMU Guest Agent MSI was not found on the VirtIO ISO'
    }
    $qemuGuestAgentInstall = Start-Process msiexec.exe -ArgumentList '/i', $qemuGuestAgentMsi.FullName, '/qn', '/norestart' -Wait -PassThru
    if ($qemuGuestAgentInstall.ExitCode -notin 0, 3010) {
        throw "QEMU Guest Agent installation failed with exit code $($qemuGuestAgentInstall.ExitCode)"
    }
}
Set-Service QEMU-GA -StartupType Automatic
Start-Service QEMU-GA

Set-NetConnectionProfile -NetworkCategory Private -ErrorAction SilentlyContinue
Enable-PSRemoting -Force
winrm set winrm/config/service '@{AllowUnencrypted="true"}'
winrm set winrm/config/service/auth '@{Basic="true"}'
Set-Service WinRM -StartupType Automatic
Restart-Service WinRM
New-NetFirewallRule -DisplayName 'Packer WinRM' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 5985 -ErrorAction SilentlyContinue
