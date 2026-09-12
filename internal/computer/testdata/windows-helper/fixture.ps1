# Opt-in acceptance fixture, executed by QGA as SYSTEM. Not an installation
# script. Secrets arrive only on stdin, never in argv, files or output.
$ErrorActionPreference='Stop'
[Console]::OutputEncoding=New-Object Text.UTF8Encoding($false)
Set-StrictMode -Version Latest
$request=[Console]::In.ReadToEnd() | ConvertFrom-Json
if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne 'S-1-5-18') { throw 'SYSTEM required' }
if ($request.marker -cnotmatch '^[a-f0-9]{24}$') { throw 'invalid fixture marker' }
$marker=$request.marker
# A failed QGA status read does not stop its PowerShell process. Serialize the
# fixture lifecycle itself so cleanup cannot race a still-running preparation
# or task registration. The random kernel object is SYSTEM-only and verified
# on reopen; no directory/file lock needs to outlive removal of the stage.
$systemSid=New-Object Security.Principal.SecurityIdentifier('S-1-5-18')
$mutexSecurity=New-Object Security.AccessControl.MutexSecurity
$mutexSecurity.SetOwner($systemSid)
$mutexSecurity.SetAccessRuleProtection($true,$false)
$mutexSecurity.AddAccessRule([Security.AccessControl.MutexAccessRule]::new($systemSid,[Security.AccessControl.MutexRights]::FullControl,[Security.AccessControl.AccessControlType]::Allow))
$createdMutex=$false
$mutex=[Threading.Mutex]::new($false,('Global\VCWFixture.'+$marker),[ref]$createdMutex,$mutexSecurity)
$acquired=$false
try {
$actualMutex=$mutex.GetAccessControl()
$mutexRules=@($actualMutex.GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier]))
if ($actualMutex.GetOwner([Security.Principal.SecurityIdentifier]).Value -ne 'S-1-5-18' -or -not $actualMutex.AreAccessRulesProtected -or $mutexRules.Count -ne 1 -or $mutexRules[0].IdentityReference.Value -ne 'S-1-5-18' -or $mutexRules[0].AccessControlType -ne 'Allow' -or $mutexRules[0].MutexRights -ne 'FullControl') { throw 'fixture mutex ownership mismatch' }
try { $acquired=$mutex.WaitOne([TimeSpan]::FromSeconds(90)) } catch [Threading.AbandonedMutexException] { $acquired=$true }
if (-not $acquired) { throw 'fixture operation still running; retain resources for recovery' }
$stage='C:\ProgramData\vcw-helper-test-'+$marker
$description='VCW acceptance '+$marker
$manifestPath=Join-Path $stage 'manifest.json'
$rootPath='SOFTWARE\VCWorkspace.ComputerV2'
$accountRootPath='SOFTWARE\VCWorkspace.AgentAccountsV2'
$registry=[Microsoft.Win32.RegistryKey]::OpenBaseKey([Microsoft.Win32.RegistryHive]::LocalMachine,[Microsoft.Win32.RegistryView]::Registry64)

function Write-NewFile([string]$path,[byte[]]$bytes) {
    $file=[IO.File]::Open($path,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None)
    try { $file.Write($bytes,0,$bytes.Length); $file.Flush($true) } finally { $file.Dispose() }
}
function Agent-Command([string]$operation,[string]$name,[string]$inputJson='') {
    if ($name -cnotmatch '^vca[a-f0-9]{12}$' -or $operation -notin @('account-provision','account-inspect','account-enable','account-disable','account-lease')) { throw 'invalid account operation' }
    $start=New-Object Diagnostics.ProcessStartInfo
    $start.FileName=Join-Path $stage 'guest.exe'
    $start.Arguments='computer-v2-'+$operation+' --guest-user '+$name
    $start.UseShellExecute=$false; $start.CreateNoWindow=$true
    $start.RedirectStandardInput=$true; $start.RedirectStandardOutput=$true; $start.RedirectStandardError=$true
    $process=[Diagnostics.Process]::Start($start)
    try {
        $stdout=$process.StandardOutput.ReadToEndAsync(); $stderr=$process.StandardError.ReadToEndAsync()
        $process.StandardInput.Write($inputJson); $process.StandardInput.Close()
        # WTS teardown has a separate 30-second Guest budget. Observation of
        # this account lifecycle must outlive it; action/consent limits do not change.
        if (-not $process.WaitForExit(45000)) { throw 'account command still running; retain fixture for recovery' }
        if ($process.ExitCode -ne 0) { throw ('Agent '+$operation+' failed: '+$stderr.Result) }
        return $stdout.Result
    } finally { $process.Dispose() }
}
function Read-Manifest {
    $dir=Get-Item -LiteralPath $stage -Force
    $acl=Get-Acl -LiteralPath $stage
    if (($dir.Attributes -band [IO.FileAttributes]::ReparsePoint) -or $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value -ne 'S-1-5-18' -or -not $acl.AreAccessRulesProtected) { throw 'unsafe fixture directory' }
    $item=Get-Item -LiteralPath $manifestPath -Force
    if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'unsafe fixture manifest' }
    $value=Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    if ($value.marker -cne $marker -or @($value.users).Count -ne 2) { throw 'fixture ownership mismatch' }
    foreach ($name in $value.users) { if ($name -cnotmatch '^vca[a-f0-9]{12}$') { throw 'invalid fixture account' } }
    return $value
}
function Owned-User([string]$name) {
    if ($name -cnotin @($manifest.users)) { throw 'not a fixture account' }
    $account=Get-LocalUser -Name $name -ErrorAction SilentlyContinue
    if ($null -ne $account) {
        $owned=Agent-Command 'account-inspect' $name | ConvertFrom-Json
        if ($owned.username -cne $name -or $owned.sid -cne $account.SID.Value) { throw 'Agent account ownership mismatch' }
        $receipt=Join-Path $stage ($name+'.json')
        if (Test-Path -LiteralPath $receipt) {
            if ((Get-Item -LiteralPath $receipt -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'unsafe SID receipt' }
            $bound=Get-Content -LiteralPath $receipt -Raw | ConvertFrom-Json
            if ($bound.username -cne $name -or $bound.sid -cne $account.SID.Value) { throw 'fixture immutable SID mismatch' }
        } else {
            # Recovery before the receipt was persisted requires the protected
            # Agent journal AND its random creation marker. Names alone fail.
            $key=$registry.OpenSubKey($accountRootPath+'\'+$name)
            if ($null -eq $key) { throw 'missing account creation journal' }
            try { $record=[Text.Encoding]::UTF8.GetString($key.GetValue('Account')) | ConvertFrom-Json } finally { $key.Dispose() }
            if ($record.sid -cne $account.SID.Value -or $account.Description -cne ('VCWorkspace-Agent-'+$record.nonce)) { throw 'unproven partial account creation' }
        }
    }
    return $account
}
function Owned-Task([string]$name,[string]$username) {
    $task=Get-ScheduledTask -TaskName $name -TaskPath '\' -ErrorAction SilentlyContinue
    if ($null -ne $task) {
        $user=Owned-User $username
        $principal=$task.Principal.UserId
        if ($principal -notmatch '^S-1-') { $principal=(New-Object Security.Principal.NTAccount($principal)).Translate([Security.Principal.SecurityIdentifier]).Value }
        if ($null -eq $user -or $task.Description -cne $description -or $principal -ne $user.SID.Value -or @($task.Actions).Count -ne 1 -or -not $task.Actions[0].Arguments.Contains($stage+'\')) { throw 'task ownership mismatch' }
    }
    return $task
}
function Logoff-OwnedUser([string]$sid) {
    if (-not ('VcwFixtureWts' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using System.Security.Principal;
public static class VcwFixtureWts {
  [StructLayout(LayoutKind.Sequential)] struct Session { public int Id; public IntPtr Name; public int State; }
  [DllImport("wtsapi32.dll", SetLastError=true)] static extern bool WTSEnumerateSessionsW(IntPtr server,int reserved,int version,out IntPtr sessions,out int count);
  [DllImport("wtsapi32.dll")] static extern void WTSFreeMemory(IntPtr p);
  [DllImport("wtsapi32.dll", SetLastError=true)] static extern bool WTSQueryUserToken(uint id,out IntPtr token);
  [DllImport("wtsapi32.dll", SetLastError=true)] static extern bool WTSLogoffSession(IntPtr server,int id,bool wait);
  [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
  public static void Logoff(string sid) {
    IntPtr sessions; int count;
    if(!WTSEnumerateSessionsW(IntPtr.Zero,0,1,out sessions,out count)) throw new Exception("WTS enumeration failed");
    try { for(int i=0;i<count;i++) {
      var session=(Session)Marshal.PtrToStructure(IntPtr.Add(sessions,i*Marshal.SizeOf(typeof(Session))),typeof(Session));
      if(session.Id==0) continue;
      IntPtr token;
      if(!WTSQueryUserToken((uint)session.Id,out token)) continue;
      try { using(var identity=new WindowsIdentity(token)) {
        if(identity.User.Value==sid && !WTSLogoffSession(IntPtr.Zero,session.Id,true)) throw new Exception("fixture logoff failed");
      }} finally { CloseHandle(token); }
    }} finally { WTSFreeMemory(sessions); }
  }
}
'@
    }
    [VcwFixtureWts]::Logoff($sid)
}

if ($request.operation -eq 'prepare') {
    if (Test-Path -LiteralPath $stage) { throw 'staging collision' }
    $existing=$registry.OpenSubKey($rootPath)
    if ($null -ne $existing) { $existing.Dispose(); throw 'requires acceptance VM with no existing V2 registry; never revoke production users' }
    $existing=$registry.OpenSubKey($accountRootPath)
    if ($null -ne $existing) { $existing.Dispose(); throw 'requires acceptance VM with no existing Agent account journal' }
    if (@($request.users).Count -ne 2 -or $request.users[0].name -ceq $request.users[1].name) { throw 'two distinct users required' }
    foreach ($user in $request.users) {
        if ($user.name -cnotmatch '^vca[a-f0-9]{12}$' -or $user.password.Length -lt 24 -or (Get-LocalUser -Name $user.name -ErrorAction SilentlyContinue)) { throw 'invalid or existing fixture user' }
    }
    $parent=Get-Item -LiteralPath 'C:\ProgramData' -Force
    if ($parent.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'unsafe staging parent' }
    $acl=New-Object Security.AccessControl.DirectorySecurity
    $acl.SetSecurityDescriptorSddlForm('O:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)')
    [IO.Directory]::CreateDirectory($stage,$acl) | Out-Null
    $manifest=[pscustomobject]@{marker=$marker;users=@($request.users | ForEach-Object {$_.name})}
    Write-NewFile $manifestPath ([Text.Encoding]::UTF8.GetBytes(($manifest | ConvertTo-Json -Compress)))
    foreach ($file in @('desktop.ps1')) { Write-NewFile (Join-Path $stage $file) ([Convert]::FromBase64String($request.scripts.$file)) }
    $download=[Net.HttpWebRequest]::Create($request.artifact_url)
    $download.Proxy=$null; $download.AllowAutoRedirect=$false; $download.Timeout=15000; $download.ReadWriteTimeout=10000
    $clock=[Diagnostics.Stopwatch]::StartNew(); $response=$download.GetResponse()
    try {
        if ([int]$response.StatusCode -ne 200 -or $response.ContentLength -ne $request.length) { throw 'invalid artifact response' }
        $source=$response.GetResponseStream(); $file=[IO.File]::Open((Join-Path $stage 'guest.exe'),[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None)
        try {
            $buffer=New-Object byte[] 65536; $total=0
            while (($read=$source.Read($buffer,0,$buffer.Length)) -gt 0) {
                $total+=$read
                if ($total -gt $request.length -or $clock.ElapsedMilliseconds -gt 45000) { throw 'artifact limit exceeded' }
                $file.Write($buffer,0,$read)
            }
            if ($total -ne $request.length) { throw 'incomplete artifact' }
            $file.Flush($true)
        } finally { $file.Dispose(); $source.Dispose() }
    } finally { $response.Dispose() }
    if ((Get-FileHash -LiteralPath (Join-Path $stage 'guest.exe') -Algorithm SHA256).Hash -ine $request.sha256) { throw 'artifact hash mismatch' }
    $accounts=@()
    foreach ($user in $request.users) {
        Agent-Command 'account-provision' $user.name | Out-Null
        $account=Owned-User $user.name
        if ($account.Enabled) { throw 'new managed account was not disabled' }
        $receipt=@{username=$account.Name;sid=$account.SID.Value}
        Write-NewFile (Join-Path $stage ($user.name+'.json')) ([Text.Encoding]::UTF8.GetBytes(($receipt | ConvertTo-Json -Compress)))
        # Test transport generates this ephemeral login secret; production
        # ownership, initial groups, registration and enablement use the Agent.
        if ($request.fenced -ne $true) {
            $secret=ConvertTo-SecureString $user.password -AsPlainText -Force
            try { $account | Set-LocalUser -Password $secret } finally { $secret.Dispose() }
            Agent-Command 'account-enable' $user.name (@{expires_unix_seconds=[DateTimeOffset]::UtcNow.AddHours(1).ToUnixTimeSeconds()} | ConvertTo-Json -Compress) | Out-Null
        }
        $accounts+=$receipt
        $rule=New-Object Security.AccessControl.FileSystemAccessRule($account.SID,'ReadAndExecute','ContainerInherit,ObjectInherit','None','Allow')
        $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $stage -AclObject $acl
    $setting=Get-CimInstance -Namespace 'root/cimv2/terminalservices' -ClassName Win32_TSGeneralSetting -Filter "TerminalName='RDP-Tcp'"
    $certificate=@(Get-ChildItem 'Cert:\LocalMachine\Remote Desktop','Cert:\LocalMachine\My' | Where-Object Thumbprint -EQ $setting.SSLCertificateSHA1Hash)
    if ($certificate.Count -ne 1) { throw 'RDP certificate must resolve uniquely' }
    $sha=[Security.Cryptography.SHA256]::Create()
    try { $fingerprint=[BitConverter]::ToString($sha.ComputeHash($certificate[0].RawData)).Replace('-','').ToLowerInvariant() } finally { $sha.Dispose() }
    # Read-only transport diagnostics; never include service command lines,
    # environment, raw QGA logs or credentials in the acceptance receipt.
    $qgaVersions=@(Get-Process -Name 'qemu-ga' -ErrorAction SilentlyContinue | ForEach-Object {
        @{session_id=$_.SessionId;file_version=[Diagnostics.FileVersionInfo]::GetVersionInfo($_.Path).FileVersion}
    })
    @{stage=$stage;users=$accounts;computer_name=[Environment]::MachineName;certificate_sha256=$fingerprint;os_version=[Environment]::OSVersion.Version.ToString();qga=$qgaVersions} | ConvertTo-Json -Depth 5 -Compress
    exit 0
}

if ($request.operation -in @('cleanup','inspect') -and -not (Test-Path -LiteralPath $stage)) {
    if ($request.PSObject.Properties.Name -notcontains 'expected_users' -or @($request.expected_users).Count -ne 2) {
        throw 'absent fixture manifest requires both explicit expected usernames; do not infer Agent ownership by prefix'
    }
    $remainingUsers=@(Get-LocalUser | Where-Object Description -CEQ $description)
    $remainingTasks=@(Get-ScheduledTask | Where-Object Description -CEQ $description)
    if ($remainingUsers.Count -ne 0 -or $remainingTasks.Count -ne 0) { throw 'fixture manifest absent but owned resources remain; inspection required' }
    if ($request.PSObject.Properties.Name -contains 'expected_users') {
        foreach ($name in @($request.expected_users)) {
            if ($name -cnotmatch '^vca[a-f0-9]{12}$') { throw 'invalid expected fixture identity' }
            if (Get-LocalUser -Name $name -ErrorAction SilentlyContinue) { throw 'expected fixture account remains' }
            foreach ($path in @($rootPath,$accountRootPath)) {
                $key=$registry.OpenSubKey($path+'\'+$name)
                if ($null -ne $key) { $key.Dispose(); throw 'expected fixture registry identity remains' }
            }
        }
    }
    Write-Output 'fixture-absent-and-no-owned-accounts-or-tasks'
    exit 0
}
$manifest=Read-Manifest
if ($request.operation -eq 'inspect') {
    $rows=@()
    foreach ($username in $manifest.users) {
        $user=Owned-User $username
        $tasks=@()
        foreach ($kind in @('desktop')) {
            $task=Owned-Task ('VCW-Test-'+$marker+'-'+$username+'-'+$kind) $username
            if ($null -ne $task) { $tasks+=@{kind=$kind;state=[string]$task.State} }
        }
        $profiles=@()
        if ($null -ne $user) { $profiles=@(Get-CimInstance Win32_UserProfile | Where-Object SID -EQ $user.SID.Value | ForEach-Object {@{loaded=$_.Loaded;special=$_.Special}}) }
        $rows+=@{username=$username;exists=($null -ne $user);tasks=$tasks;profiles=$profiles}
    }
    @{marker=$marker;users=$rows;files=@(Get-ChildItem -LiteralPath $stage -Force | Select-Object -ExpandProperty Name)} | ConvertTo-Json -Depth 6 -Compress
    exit 0
}
if ($request.operation -eq 'desktop-ready') {
    $user=Owned-User $request.username
    if ($null -eq $user -or $user.SID.Value -cne $request.sid) { throw 'fixture readiness SID mismatch' }
    $profile=Get-CimInstance Win32_UserProfile | Where-Object SID -EQ $user.SID.Value
    if ($null -ne $profile) {
        $readyPath=Join-Path $profile.LocalPath 'AppData\Local\VCWFixtureReady.txt'
        if ((Test-Path -LiteralPath $readyPath) -and (Get-Content -LiteralPath $readyPath -Raw) -ceq $user.SID.Value) { Write-Output 'fixture-visible'; exit 0 }
    }
    Write-Output 'fixture-starting'
    exit 0
}
if ($request.operation -eq 'diagnostics') {
    $rows=@()
    foreach ($username in $manifest.users) {
        $user=Owned-User $username
        $profile=Get-CimInstance Win32_UserProfile | Where-Object SID -EQ $user.SID.Value
        if ($null -ne $profile) {
            $errorPath=Join-Path $profile.LocalPath 'AppData\Local\VCWFixtureError.txt'
            if (Test-Path -LiteralPath $errorPath) {
                $message=Get-Content -LiteralPath $errorPath -Raw
                $rows+=@{username=$username;fixture_error=$message.Substring(0,[Math]::Min(500,$message.Length))}
            }
        }
        foreach ($kind in @('desktop')) {
            $task=Owned-Task ('VCW-Test-'+$marker+'-'+$username+'-'+$kind) $username
            if ($null -ne $task) {
                $info=$task | Get-ScheduledTaskInfo
                $rows+=@{username=$username;kind=$kind;state=[string]$task.State;last_result=$info.LastTaskResult}
            }
        }
    }
    $rows | ConvertTo-Json -Compress
    exit 0
}
if ($request.operation -eq 'tasks') {
    foreach ($username in $manifest.users) {
        $user=Owned-User $username
        if ($null -eq $user) { throw 'fixture user absent' }
        foreach ($kind in @('desktop')) {
            $name='VCW-Test-'+$marker+'-'+$username+'-'+$kind
            if (Get-ScheduledTask -TaskName $name -TaskPath '\' -ErrorAction SilentlyContinue) { throw 'task collision' }
            $arguments='-NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File "'+$stage+'\'+$kind+'.ps1" -Username '+$username
            $action=New-ScheduledTaskAction -Execute 'C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe' -Argument $arguments
            $principal=New-ScheduledTaskPrincipal -UserId $user.SID.Value -LogonType Interactive -RunLevel Limited
            $trigger=New-ScheduledTaskTrigger -AtLogOn -User $user.SID.Value
            $settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
            Register-ScheduledTask -TaskName $name -TaskPath '\' -Action $action -Principal $principal -Trigger $trigger -Settings $settings -Description $description | Out-Null
        }
    }
    exit 0
}
if ($request.operation -eq 'logoff') {
    $user=Owned-User $request.username
    if ($null -eq $user -or $user.SID.Value -cne $request.sid) { throw 'logoff SID mismatch' }
    Logoff-OwnedUser $user.SID.Value
    exit 0
}
if ($request.operation -eq 'cleanup') {
    # Stop only marker-owned tasks and log off only their current full SID.
    foreach ($username in $manifest.users) {
        foreach ($kind in @('desktop')) {
            $name='VCW-Test-'+$marker+'-'+$username+'-'+$kind
            $task=Owned-Task $name $username
            if ($null -ne $task) { $task | Stop-ScheduledTask; $task | Unregister-ScheduledTask -Confirm:$false }
        }
        $user=Owned-User $username
        if ($null -ne $user) {
            $receipt=Join-Path $stage ($username+'.json')
            if (-not (Test-Path -LiteralPath $receipt)) {
                Write-NewFile $receipt ([Text.Encoding]::UTF8.GetBytes((@{username=$username;sid=$user.SID.Value} | ConvertTo-Json -Compress)))
            }
            $observed=Agent-Command 'account-inspect' $username | ConvertFrom-Json
            if ($null -ne $observed.lifecycle) {
                $fence=$observed.lifecycle
                if ($fence.schema_version -ne 1 -or $fence.identity.username -cne $username -or $fence.identity.sid -cne $user.SID.Value -or $fence.identity.uid -ne 0 -or $fence.lease_id -cne ('lease_'+$marker) -or $fence.control_epoch -notin @(1,2)) { throw 'unexpected fixture account lifecycle; retain' }
                if ($fence.login_generation -ne 1) { throw 'unexpected fixture login generation; retain' }
                Agent-Command 'account-lease' $username (@{schema_version=1;identity=$fence.identity;lease_id=$fence.lease_id;control_epoch=$fence.control_epoch;login_generation=$fence.login_generation;expires_unix_seconds=0;operation='revoke'} | ConvertTo-Json -Depth 4 -Compress) | Out-Null
            } else {
                Agent-Command 'account-disable' $username | Out-Null
            }
        }
    }
    $root=$registry.OpenSubKey($rootPath,$true)
    if ($null -ne $root) {
        try {
            $security=$root.GetAccessControl()
            if ($security.GetOwner([Security.Principal.SecurityIdentifier]).Value -ne 'S-1-5-18' -or -not $security.AreAccessRulesProtected -or $root.GetValueNames() -contains 'SymbolicLinkValue') { throw 'unsafe registry root; retain for inspection' }
            foreach ($name in $root.GetSubKeyNames()) {
                $user=Owned-User $name
                if ($null -eq $user) { throw 'unexpected V2 account; refuse registry cleanup' }
                $key=$root.OpenSubKey($name)
                try {
                    if ($key.GetSubKeyNames().Count -ne 0 -or $key.GetValueNames() -contains 'SymbolicLinkValue' -or [Text.Encoding]::UTF8.GetString($key.GetValue('SID')) -cne $user.SID.Value) { throw 'registry SID mismatch; retain for inspection' }
                } finally { $key.Dispose() }
                $root.DeleteSubKey($name,$true)
            }
        } finally { $root.Dispose() }
        $registry.DeleteSubKey($rootPath,$true)
    }
    foreach ($username in $manifest.users) {
        $user=Owned-User $username
        if ($null -eq $user) { continue }
        $profiles=@(Get-CimInstance Win32_UserProfile | Where-Object SID -EQ $user.SID.Value)
        foreach ($profile in $profiles) {
            if ($profile.Loaded -or $profile.Special) { throw 'fixture profile still in use; retain account' }
            $profile | Remove-CimInstance
        }
        $user | Remove-LocalUser
    }
    $root=$registry.OpenSubKey($accountRootPath,$true)
    if ($null -ne $root) {
        try {
            $security=$root.GetAccessControl()
            if ($security.GetOwner([Security.Principal.SecurityIdentifier]).Value -ne 'S-1-5-18' -or -not $security.AreAccessRulesProtected -or $root.GetValueNames() -contains 'SymbolicLinkValue') { throw 'unsafe account journal; retain' }
            foreach ($name in $root.GetSubKeyNames()) {
                if ($name -cnotin $manifest.users -or (Get-LocalUser -Name $name -ErrorAction SilentlyContinue)) { throw 'unrelated or remaining account; retain journal' }
                $key=$root.OpenSubKey($name)
                try {
                    if ($key.GetSubKeyNames().Count -ne 0 -or $key.GetValueNames() -contains 'SymbolicLinkValue') { throw 'unsafe account journal leaf' }
                    $record=[Text.Encoding]::UTF8.GetString($key.GetValue('Account')) | ConvertFrom-Json
                    if ($record.schema_version -ne 1 -or $record.username -cne $name) { throw 'account journal identity mismatch' }
                    if ($null -ne $record.sid) {
                        $receipt=Get-Content -LiteralPath (Join-Path $stage ($name+'.json')) -Raw | ConvertFrom-Json
                        if ($receipt.username -cne $name -or $receipt.sid -cne $record.sid) { throw 'account journal does not match removed fixture SID' }
                    }
                } finally { $key.Dispose() }
                $root.DeleteSubKey($name,$true)
            }
        } finally { $root.Dispose() }
        $registry.DeleteSubKey($accountRootPath,$true)
    }
    foreach ($name in (@('guest.exe','desktop.ps1','manifest.json')+@($manifest.users | ForEach-Object {$_+'.json'}))) {
        $path=Join-Path $stage $name
        if (Test-Path -LiteralPath $path) {
            if ((Get-Item -LiteralPath $path -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'unexpected staging link; retain' }
            Remove-Item -LiteralPath $path -Force
        }
    }
    [IO.Directory]::Delete($stage,$false)
    Write-Output 'fixture-cleaned'
    exit 0
}
throw 'unknown fixture operation'
} finally {
    if ($acquired) { $mutex.ReleaseMutex() }
    $mutex.Dispose()
}
