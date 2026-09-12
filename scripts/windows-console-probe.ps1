# Reads back what a program wrote into the console it was given.
#
# A GUI-subsystem program launched from here is handed no standard handles it can use, so anything that turns up in
# this console's screen buffer got there because the program attached to the caller's console and opened CONOUT$
# (docs/adr/0017). This is the only way to prove that on a build runner, which has no interactive session.
param([Parameter(Mandatory)][string]$Exe, [Parameter(Mandatory)][string]$Out)
& $Exe version
$ui = $Host.UI.RawUI
$end = [Math]::Max($ui.CursorPosition.Y, 1)
$w = $ui.BufferSize.Width
$cells = $ui.GetBufferContents((New-Object System.Management.Automation.Host.Rectangle 0, 0, ($w - 1), $end))
$sb = New-Object Text.StringBuilder
for ($y = 0; $y -le $end; $y++) {
  for ($x = 0; $x -lt $w; $x++) { [void]$sb.Append($cells[$y, $x].Character) }
  [void]$sb.AppendLine()
}
Set-Content -Path $Out -Value $sb.ToString()
