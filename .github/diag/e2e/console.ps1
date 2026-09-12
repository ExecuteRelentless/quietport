# Diagnostic for 0.1.22: the background agent must not leave a console window on a member's screen.
#   shipped  the published agent, as a control: its window MUST be visible, or this check proves nothing
#   fix      this branch's agent: the window must be gone, and the agent must still work
# The agent is started the way a member's install starts it: by the Scheduled Task the installer registers, with an
# interactive token. That is what opens the window; starting the agent from this script would not reproduce it.
# .NET reports MainWindowHandle only for a top-level window that is visible, so a hidden console reads as 0.
$ErrorActionPreference = 'Continue'
$E = 'C:\Users\Public\qpe2e'
$V = (Get-Content "$E\variant.txt" -Raw).Trim()
$RUN = (Get-Content "$E\runid.txt" -Raw).Trim()
$Q = Join-Path $env:LOCALAPPDATA 'Quietport'
$S = Join-Path $env:USERPROFILE 'QPSync'
"QPC: user=$env:USERNAME variant=$V"

function Show-Agents($label) {
    $ps = @(Get-Process qpsync-agent -ErrorAction SilentlyContinue)
    $visible = 0
    "QPC: agents ${label}: $($ps.Count)"
    foreach ($p in $ps) {
        $p.Refresh()
        $h = [int64]$p.MainWindowHandle
        if ($h -ne 0) { $visible++ }
        "QPC:   pid=$($p.Id) MainWindowHandle=$h title='$($p.MainWindowTitle)'"
    }
    "QPC: RESULT ${label}: variant=$V agents=$($ps.Count) withVisibleWindow=$visible"
}

# --- stage the app folder the way the installer leaves it ------------------------------------------------------
New-Item -ItemType Directory -Force $Q | Out-Null
Copy-Item "$E\bundle\*" $Q -Force
Copy-Item "$E\agent\qpsync-agent.exe" "$Q\qpsync-agent.exe" -Force
$I = Join-Path $E 'installer'
New-Item -ItemType Directory -Force $I | Out-Null
Copy-Item "$E\agent\qpsync-agent.exe" "$I\Quietport.exe" -Force
Get-FileHash "$Q\qpsync-agent.exe" | ForEach-Object { "QPC: agent sha256 $($_.Hash.Substring(0,16))" }

# --- install: this registers the Scheduled Task and runs it ----------------------------------------------------
$code = (Get-Content "$E\code1.txt" -Raw).Trim()
'QPC: ===== install'
& "$I\Quietport.exe" install --code $code --payload "$E\payload1.json"
"QPC: install exit=$LASTEXITCODE"
Start-Sleep -Seconds 20

'QPC: ===== the scheduled task the installer registered'
$q = schtasks /Query /TN Quietport /V /FO LIST 2>&1
$q | Select-String -Pattern 'Status:|Task To Run:|Logon Mode:|Run As User:|Scheduled Task State:' | ForEach-Object { "QPC:   $($_.Line.Trim())" }

Show-Agents 'after the install started the task'

# --- the agent must still do its job with the console hidden ---------------------------------------------------
$folder = Get-ChildItem $S -Directory -ErrorAction SilentlyContinue | Select-Object -First 1
if ($folder) {
    $proof = "qpc-proof-$RUN-$V.txt"
    Set-Content -Path (Join-Path $folder.FullName $proof) -Value "Quietport 0.1.22 console diagnostic $RUN $V $(Get-Date -Format o)"
    "QPC: proof file written: $proof in $($folder.Name)"
    Start-Sleep -Seconds 70
    $files = @(Get-ChildItem $folder.FullName -Recurse -File -Force -ErrorAction SilentlyContinue | Where-Object { $_.FullName -notlike '*\.qp-versions\*' })
    "QPC: folder at the end: $($folder.Name), $($files.Count) files"
    foreach ($x in $files) { "QPC:   file $($x.Name)" }
} else {
    'QPC: no circle folder after the install'
}

Show-Agents 'at the end'
'QPC: ===== agent status'
& "$Q\qpsync-agent.exe" status
'QPC: ===== agent.log (tailscaled lines left out, keys masked)'
Get-Content "$Q\logs\agent.log" -ErrorAction SilentlyContinue |
    Where-Object { $_ -notmatch 'tailscaled:' } |
    ForEach-Object { $_ -replace '(?i)(auth[-_ ]?key[=:" ]+)[^\s",]+', '$1[masked]' }
schtasks /End /TN Quietport 2>&1 | Out-Null
Get-Process qpsync-agent, rclone, 'Quietport Network' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
'QPC: done'
