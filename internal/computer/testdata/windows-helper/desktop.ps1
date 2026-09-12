param([Parameter(Mandatory=$true)][string]$Username)
$ErrorActionPreference='Stop'
trap {
    $errorPath=Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'VCWFixtureError.txt'
    [IO.File]::WriteAllText($errorPath,($_.Exception.Message+' at '+$_.InvocationInfo.ScriptLineNumber))
    exit 1
}
if ($Username -cnotmatch '^vca[a-f0-9]{12}$') { throw 'invalid fixture user' }
# Windows PowerShell can retain the pre-4.6.1 WinForms compatibility default.
# Opt this synthetic application into the framework's built-in Ctrl+A command
# before creating any controls. Do not handle Ctrl+A or select/assign test text
# here: the real injected key must still reach WinForms and change the selection.
# https://learn.microsoft.com/dotnet/core/compatibility/fx-core#donotsupportselectallshortcutinmultilinetextbox-compatibility-switch-not-supported
[AppContext]::SetSwitch('Switch.System.Windows.Forms.DoNotSupportSelectAllShortcutInMultilineTextBox',$false)
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$form=New-Object Windows.Forms.Form
$form.Text='VCW private desktop '+$Username
$form.Width=800; $form.Height=450; $form.StartPosition='CenterScreen'
$form.TopMost=$true
$inputBox=New-Object Windows.Forms.TextBox
$inputBox.Multiline=$true; $inputBox.WordWrap=$false; $inputBox.ShortcutsEnabled=$true
$inputBox.AccessibleName='VCW_INPUT_'+$Username
$inputBox.SetBounds(40,80,680,40)
$inputBox.Font=New-Object Drawing.Font('Segoe UI',16)
$echo=New-Object Windows.Forms.Label
$echo.SetBounds(40,150,680,100)
$echo.Text='VCW_ECHO:'; $echo.AccessibleName=$echo.Text
$echo.Font=New-Object Drawing.Font('Segoe UI',14)
$status=New-Object Windows.Forms.Label
$status.SetBounds(40,280,680,60)
$status.Text='VCW_STATUS:starting'; $status.AccessibleName=$status.Text
$script:fixtureClicks=0
$script:fixtureKeyDowns=0
$script:fixtureLastKey='none'
$inputBox.Add_MouseDown({ $script:fixtureClicks++ })
$inputBox.Add_KeyDown({ param($sender,$keyEvent)
    $script:fixtureKeyDowns++
    $script:fixtureLastKey=$keyEvent.KeyData.ToString()
})
$timer=New-Object Windows.Forms.Timer
$timer.Interval=250
$timer.Add_Tick({
    $cursor=[Windows.Forms.Cursor]::Position
    $status.Text='VCW_STATUS:focused='+$inputBox.Focused+';cursor='+$cursor.X+','+$cursor.Y+';clicks='+$script:fixtureClicks+';selection='+$inputBox.SelectionStart+','+$inputBox.SelectionLength+';keys='+$script:fixtureKeyDowns+';last='+$script:fixtureLastKey
    $status.AccessibleName=$status.Text
})
# Only public test input is mirrored into a label. Production UIA deliberately
# omits ValuePattern text, and this fixture must not weaken that privacy rule.
$inputBox.Add_TextChanged({ $echo.Text='VCW_ECHO:'+$inputBox.Text; $echo.AccessibleName=$echo.Text })
$form.Controls.Add($inputBox); $form.Controls.Add($echo); $form.Controls.Add($status)
$form.Add_Shown({
    $form.Activate(); $inputBox.Focus()
    $timer.Start()
    $readyPath=Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'VCWFixtureReady.txt'
    [IO.File]::WriteAllText($readyPath,[Security.Principal.WindowsIdentity]::GetCurrent().User.Value)
})
[Windows.Forms.Application]::Run($form)
$timer.Dispose()
