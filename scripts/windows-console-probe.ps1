# Reads back what a program wrote into the console it was given.
#
# A GUI-subsystem program launched from here is handed no standard handles it can use, so anything of its that turns
# up in this console's screen buffer got there because the program attached to the caller's console and opened
# CONOUT$ (docs/adr/0017). This is the only way to see that on a build runner, which has no interactive session.
#
# PROBE-READY is the probe's own mark. If it is missing from the capture then this console could not be read at all,
# and the run says nothing either way: a capture that comes back empty for every program is a blind harness, not a
# program that printed nothing.
param([Parameter(Mandatory)][string]$Exe, [Parameter(Mandatory)][string]$Out)
Write-Host "PROBE-READY"
# Started this way, not with "& $Exe", because PowerShell does not wait for a GUI-subsystem program unless its
# output is going somewhere: "&" would return before the program had written a thing and the read below would race
# it. UseShellExecute stays false and nothing is redirected, so the program is launched exactly as "&" launches it.
$psi = New-Object Diagnostics.ProcessStartInfo
$psi.FileName = $Exe
$psi.Arguments = "version"
$psi.UseShellExecute = $false
$p = [Diagnostics.Process]::Start($psi)
$p.WaitForExit()
$ui = $Host.UI.RawUI
$end = [Math]::Min([Math]::Max($ui.CursorPosition.Y, 1), $ui.BufferSize.Height - 1)
$w = $ui.BufferSize.Width
$cells = $ui.GetBufferContents((New-Object System.Management.Automation.Host.Rectangle 0, 0, ($w - 1), $end))
$sb = New-Object Text.StringBuilder
for ($y = 0; $y -le $end; $y++) {
  for ($x = 0; $x -lt $w; $x++) { [void]$sb.Append($cells[$y, $x].Character) }
  [void]$sb.AppendLine()
}
Set-Content -Path $Out -Value $sb.ToString()
