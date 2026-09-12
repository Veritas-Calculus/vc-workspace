$ErrorActionPreference = 'Stop'

$payloadRoot = $PSScriptRoot
$cloudbaseInitMsi = Join-Path $payloadRoot 'CloudbaseInitSetup.msi'
$agentBinary = Join-Path $payloadRoot 'vc-workspace-guest-agent-windows-amd64.exe'
if (-not (Test-Path $cloudbaseInitMsi)) {
    throw 'Cloudbase-Init MSI was not found on the VC Workspace build payload CD-ROM'
}
if (-not (Test-Path $agentBinary)) {
    throw 'VC Workspace guest agent was not found on the build payload CD-ROM'
}
$backgroundSource = Join-Path $payloadRoot 'vc-workspace-desktop-background.png'
if (-not (Test-Path $backgroundSource)) {
    throw 'VC Workspace desktop background was not found on the build payload CD-ROM'
}

$cloudbaseInstall = Start-Process msiexec.exe -ArgumentList '/i', $cloudbaseInitMsi, '/qn', '/norestart', 'RUN_SERVICE_AS_LOCAL_SYSTEM=1' -Wait -PassThru
if ($cloudbaseInstall.ExitCode -notin 0, 3010) {
    throw "Cloudbase-Init installation failed with exit code $($cloudbaseInstall.ExitCode)"
}

Set-ItemProperty -Path 'HKLM:\System\CurrentControlSet\Control\Terminal Server' -Name fDenyTSConnections -Value 0
Set-ItemProperty -Path 'HKLM:\System\CurrentControlSet\Control\Terminal Server\WinStations\RDP-Tcp' -Name UserAuthentication -Value 1
Remove-NetFirewallRule -DisplayName 'VC Workspace RDP' -ErrorAction SilentlyContinue
New-NetFirewallRule -DisplayName 'VC Workspace RDP' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 3389 -Profile Any | Out-Null
Set-Service TermService -StartupType Automatic

$brandDirectory = Join-Path $env:ProgramData 'VC Workspace\Brand'
New-Item -ItemType Directory -Path $brandDirectory -Force | Out-Null
$backgroundPath = Join-Path $brandDirectory 'desktop-background.png'
Copy-Item -LiteralPath $backgroundSource -Destination $backgroundPath -Force
$backgroundPolicy = Join-Path $brandDirectory 'background-policy'
Set-Content -LiteralPath $backgroundPolicy -Value 'managed' -Encoding Ascii
$backgroundScript = Join-Path $brandDirectory 'Apply-DesktopBackground.ps1'
@'
$ErrorActionPreference = 'SilentlyContinue'
$policy = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Policies\System'
$policyMode = (Get-Content -LiteralPath 'C:\ProgramData\VC Workspace\Brand\background-policy' -Raw).Trim()
if ($policyMode -eq 'managed') {
    if (-not (Test-Path -LiteralPath $policy)) { New-Item -Path $policy -Force | Out-Null }
    Set-ItemProperty -Path $policy -Name Wallpaper -Value 'C:\ProgramData\VC Workspace\Brand\desktop-background.png' -Type String
    Set-ItemProperty -Path $policy -Name WallpaperStyle -Value '10' -Type String
    Set-ItemProperty -Path $policy -Name TileWallpaper -Value '0' -Type String
} elseif (Test-Path -LiteralPath $policy) {
    Remove-ItemProperty -Path $policy -Name Wallpaper,WallpaperStyle,TileWallpaper -ErrorAction SilentlyContinue
}
& "$env:SystemRoot\System32\RUNDLL32.EXE" user32.dll,UpdatePerUserSystemParameters 1, True
'@ | Set-Content -LiteralPath $backgroundScript -Encoding UTF8
$runKey = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Run'
New-ItemProperty -Path $runKey -Name 'VCWorkspaceBackground' -PropertyType String -Value ('powershell.exe -NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File "{0}"' -f $backgroundScript) -Force | Out-Null

$identityCapabilities = @{
    schema_version = 1
    platform = 'windows'
    modes = @('managed_local', 'windows_ad', 'windows_entra')
    windows_entra = 'experimental'
} | ConvertTo-Json -Depth 3
$identityCapabilityDirectory = Join-Path $env:ProgramData 'VC Workspace'
New-Item -ItemType Directory -Path $identityCapabilityDirectory -Force | Out-Null
Set-Content -LiteralPath (Join-Path $identityCapabilityDirectory 'identity-capabilities.json') -Value $identityCapabilities -Encoding UTF8

# Sysprep's unattended OOBE settings do not suppress every post-logon
# privacy page on current Windows 11 media. This device policy prevents the
# privacy experience from interrupting the first VDI RDP session.
$oobePolicy = 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\OOBE'
if (-not (Test-Path -LiteralPath $oobePolicy)) {
    New-Item -Path $oobePolicy -Force | Out-Null
}
New-ItemProperty -Path $oobePolicy -Name DisablePrivacyExperience -PropertyType DWord -Value 1 -Force | Out-Null

# VDI desktops use explicit power operations. Disabling hibernation also
# disables Fast Startup, which otherwise preserves PCI device state across a
# shutdown and can make a passed-through GPU fail on the next cold start.
& powercfg.exe /hibernate off
if ($LASTEXITCODE -ne 0) {
    throw "Disabling Windows hibernation failed with exit code $LASTEXITCODE"
}

# Windows 11 can silently enable Device Encryption when a TPM is present.
# Sysprep refuses to generalize an encrypted OS volume, and a cloned VDI
# desktop must not inherit host-local recovery material from the template.
$bitLockerPolicy = 'HKLM:\SYSTEM\CurrentControlSet\Control\BitLocker'
if (-not (Test-Path -LiteralPath $bitLockerPolicy)) {
    New-Item -Path $bitLockerPolicy | Out-Null
}
New-ItemProperty -Path $bitLockerPolicy -Name PreventDeviceEncryption -PropertyType DWord -Value 1 -Force | Out-Null
$bitLockerVolume = Get-BitLockerVolume -MountPoint 'C:'
if ($bitLockerVolume.VolumeStatus -ne 'FullyDecrypted') {
    Disable-BitLocker -MountPoint 'C:' | Out-Null
    $decryptionDeadline = (Get-Date).AddMinutes(20)
    do {
        Start-Sleep -Seconds 5
        $bitLockerVolume = Get-BitLockerVolume -MountPoint 'C:'
        if ((Get-Date) -ge $decryptionDeadline) {
            throw "BitLocker decryption timed out at $($bitLockerVolume.EncryptionPercentage)%"
        }
    } while ($bitLockerVolume.VolumeStatus -ne 'FullyDecrypted')
}

$vdiPassword = ([guid]::NewGuid().ToString('N') + 'aA1!')
if (Get-LocalUser -Name 'vdi' -ErrorAction SilentlyContinue) {
    Set-LocalUser -Name 'vdi' -Password (ConvertTo-SecureString $vdiPassword -AsPlainText -Force) -AccountNeverExpires
} else {
    New-LocalUser -Name 'vdi' -Password (ConvertTo-SecureString $vdiPassword -AsPlainText -Force) -AccountNeverExpires -PasswordNeverExpires
}
$remoteDesktopUsers = Get-LocalGroup | Where-Object { $_.SID.Value -eq 'S-1-5-32-555' } | Select-Object -First 1
if (-not $remoteDesktopUsers) {
    throw 'The built-in Remote Desktop Users group was not found'
}
Add-LocalGroupMember -Group $remoteDesktopUsers.Name -Member 'vdi' -ErrorAction SilentlyContinue

$agentDirectory = Join-Path $env:ProgramFiles 'VC Workspace\Agent'
$stateDirectory = Join-Path $env:ProgramData 'VC Workspace\Agent'
$computerDirectory = Join-Path $stateDirectory 'computer'
$computerRequests = Join-Path $computerDirectory 'requests'
$computerResponses = Join-Path $computerDirectory 'responses'
New-Item -ItemType Directory -Path $agentDirectory, $stateDirectory, $computerDirectory, $computerRequests, $computerResponses -Force | Out-Null
$vdiSID = (Get-LocalUser -Name 'vdi').SID.Value
& icacls.exe $computerDirectory /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' ("*$vdiSID`:(OI)(CI)M") | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "Securing the Computer Use spool failed with exit code $LASTEXITCODE"
}
Copy-Item $agentBinary (Join-Path $agentDirectory 'vc-workspace-guest-agent.exe') -Force
$agentExecutable = Join-Path $agentDirectory 'vc-workspace-guest-agent.exe'
$agentAction = New-ScheduledTaskAction -Execute $agentExecutable -Argument ('--state-dir "{0}"' -f $stateDirectory)
$agentTrigger = New-ScheduledTaskTrigger -AtStartup
$agentPrincipal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$taskSettings = New-ScheduledTaskSettingsSet -RestartCount 10 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit (New-TimeSpan -Days 3650) -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName 'VC Workspace Guest Agent' -Action $agentAction -Trigger $agentTrigger -Principal $agentPrincipal -Settings $taskSettings -Force | Out-Null
$helperLog = Join-Path $computerDirectory 'helper.log'
$helperHost = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$helperArguments = '-NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -Command "& ''{0}'' computer-helper --state-dir ''{1}'' *>> ''{2}''"' -f $agentExecutable, $stateDirectory, $helperLog
$helperAction = New-ScheduledTaskAction -Execute $helperHost -Argument $helperArguments
$helperTrigger = New-ScheduledTaskTrigger -AtLogOn -User 'vdi'
$helperPrincipal = New-ScheduledTaskPrincipal -UserId ("{0}\vdi" -f $env:COMPUTERNAME) -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName 'VC Workspace Computer Helper' -Action $helperAction -Trigger $helperTrigger -Principal $helperPrincipal -Settings $taskSettings -Force | Out-Null

Set-Service QEMU-GA -StartupType Automatic
Remove-NetFirewallRule -DisplayName 'Packer WinRM' -ErrorAction SilentlyContinue
