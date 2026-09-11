# Diagnostic for 0.1.21 (ADR 0015, ADR 0016). Runs as the standard local user qpstd (PsExec, real logon, profile
# loaded), the way a member's install runs. One of three variants, from variant.txt:
#   fresh     a first install: the daemon must run as "Quietport Network.exe" and the folder must sync
#   update    an install on 0.1.20, then the state a self-update leaves behind (new agent binary, the old daemon
#             still running under the old name): the new agent must rename the file, stop that daemon, and stay online
#   reinstall a second install onto a folder holding files this computer has no record of: they must move aside and
#             never reach the hub. Also: a tailscaled.exe running OUTSIDE the app folder must survive the install,
#             and an install run from inside the app folder must not kill itself.
# Every line the operator reads starts with QPD:. What reached the hub is checked in the circle's bucket afterwards.
$ErrorActionPreference = 'Continue'
$E = 'C:\Users\Public\qpe2e'
$V = (Get-Content "$E\variant.txt" -Raw).Trim()
$RUN = (Get-Content "$E\runid.txt" -Raw).Trim()
$Q = Join-Path $env:LOCALAPPDATA 'Quietport'
$S = Join-Path $env:USERPROFILE 'QPSync'
$DAEMON = 'Quietport Network.exe'
"QPD: user=$env:USERNAME variant=$V"

function Show-Procs($label) {
    $ps = @(Get-Process -ErrorAction SilentlyContinue |
        Where-Object { $_.ProcessName -in @('qpsync-agent', 'rclone', 'tailscaled', 'Quietport Network', 'Quietport') })
    "QPD: processes ${label}: $($ps.Count)"
    foreach ($p in $ps) {
        $path = try { $p.Path } catch { '(path not readable)' }
        "QPD:   pid=$($p.Id) name=$($p.ProcessName) path=$path"
    }
}

function Stop-Ours {
    # the harness's own cleanup, by name: the point of the test is what the AGENT stops, not what this script stops
    Get-Process qpsync-agent, rclone, tailscaled, 'Quietport Network' -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2
}

function Get-CircleFolder {
    Get-ChildItem $S -Directory -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -notlike 'Previous files*' } | Select-Object -First 1
}

function Show-Folder($label) {
    $f = Get-CircleFolder
    if (-not $f) { "QPD: folder ${label}: none"; return }
    $files = @(Get-ChildItem $f.FullName -Recurse -File -Force -ErrorAction SilentlyContinue |
        Where-Object { $_.FullName -notlike '*\.qp-versions\*' })
    $mb = [math]::Round((($files | Measure-Object Length -Sum).Sum) / 1MB, 1)
    "QPD: folder ${label}: $($f.Name), $($files.Count) files, $mb MB"
    foreach ($x in $files) { "QPD:   file $($x.Name)" }
}

function Show-Daemon($label) {
    "QPD: daemon file ${label}: '$DAEMON' exists=$(Test-Path (Join-Path $Q $DAEMON)), tailscaled.exe exists=$(Test-Path (Join-Path $Q 'tailscaled.exe'))"
}

function Show-Log {
    'QPD: ===== agent.log (tailscaled lines left out, keys masked)'
    Get-Content "$Q\logs\agent.log" -ErrorAction SilentlyContinue |
        Where-Object { $_ -notmatch 'tailscaled:' } |
        ForEach-Object { $_ -replace '(?i)(auth[-_ ]?key[=:" ]+)[^\s",]+', '$1[masked]' }
}

function Install-From($exe, $n) {
    $code = (Get-Content "$E\code$n.txt" -Raw).Trim()
    "QPD: ===== install $n using $exe"
    & $exe install --code $code --payload "$E\payload$n.json"
    "QPD: install $n exit=$LASTEXITCODE"
}

# --- staging: the app folder, as the installer would leave it -------------------------------------------------
New-Item -ItemType Directory -Force $Q | Out-Null
Copy-Item "$E\bundle\*" $Q -Force
Copy-Item "$E\agent\qpsync-agent.exe" "$Q\qpsync-agent.exe" -Force
$I = Join-Path $E 'installer'
New-Item -ItemType Directory -Force $I | Out-Null
Copy-Item "$E\agent\qpsync-agent.exe" "$I\Quietport.exe" -Force
Show-Daemon 'staged'

# --- the first install ---------------------------------------------------------------------------------------
Install-From "$I\Quietport.exe" 1
Stop-Ours
Show-Daemon 'after install 1'
$folder = Get-CircleFolder
if (-not $folder) { 'QPD: no circle folder after install 1'; Show-Log; 'QPD: done'; exit }

if ($V -eq 'reinstall') {
    # files this computer has no record of: one must survive the reinstall, and NEITHER may reach the hub
    Set-Content -Path (Join-Path $folder.FullName "do-not-upload-$RUN.txt") -Value "must never reach the hub ($RUN)"
    New-Item -ItemType Directory -Force (Join-Path $folder.FullName 'Old notes') | Out-Null
    Set-Content -Path (Join-Path $folder.FullName 'Old notes\note.txt') -Value "also must never reach the hub ($RUN)"
    Show-Folder 'before the reinstall'

    # a real Tailscale install elsewhere on the computer: a stand-in running from outside the app folder. 0.1.20 ran
    # taskkill /IM tailscaled.exe, which would have ended this one.
    $fake = Join-Path $E 'faketailscale'
    New-Item -ItemType Directory -Force $fake | Out-Null
    Copy-Item "$env:SystemRoot\System32\PING.EXE" "$fake\tailscaled.exe" -Force
    $decoy = Start-Process -FilePath "$fake\tailscaled.exe" -ArgumentList '127.0.0.1', '-t' -PassThru -WindowStyle Hidden
    "QPD: decoy tailscaled.exe started outside the app folder: pid=$($decoy.Id)"

    # Quietport's own programs have to be running, or the install has nothing to stop
    Start-Process -FilePath "$Q\qpsync-agent.exe" -ArgumentList run -WorkingDirectory 'C:\Windows\System32' -WindowStyle Hidden
    Start-Sleep -Seconds 30
    $ours = @(Get-Process qpsync-agent, 'Quietport Network', rclone -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Id)
    "QPD: our pids before the reinstall: $($ours -join ', ')"

    # A retried install: the config is here, the enrolment never finished, so this computer has no device of its own
    # and no record of what made the folder. Deleting the config instead would leave no port, and Install would then
    # stop nothing at all, which is the half of this that must be exercised.
    $cfgPath = Join-Path $Q 'config.json'
    $cfg = Get-Content $cfgPath -Raw | ConvertFrom-Json
    $cfg.device_id = 0
    [IO.File]::WriteAllText($cfgPath, ($cfg | ConvertTo-Json -Depth 20))  # WriteAllText: UTF-8 with no BOM, which Go reads
    "QPD: device_id set to 0 (a retried install)"

    # run from INSIDE the app folder: 0.1.20's taskkill /IM qpsync-agent.exe killed this very process
    Install-From "$Q\qpsync-agent.exe" 2
    Show-Procs 'after install 2'
    "QPD: decoy still running after the install: $($null -ne (Get-Process -Id $decoy.Id -ErrorAction SilentlyContinue))"
    foreach ($was in $ours) {
        "QPD: our earlier pid $was stopped by the install: $($null -eq (Get-Process -Id $was -ErrorAction SilentlyContinue))"
    }
    $aside = @(Get-ChildItem $S -Directory -ErrorAction SilentlyContinue | Where-Object { $_.Name -like 'Previous files*' })
    "QPD: set-aside folders: $($aside.Count)"
    foreach ($a in $aside) {
        foreach ($x in Get-ChildItem $a.FullName -Recurse -File -Force -ErrorAction SilentlyContinue) {
            "QPD:   set aside $($x.FullName.Substring($S.Length + 1))"
        }
    }
    Show-Folder 'after the reinstall'
    Stop-Ours
    $folder = Get-CircleFolder
}

if ($V -eq 'update') {
    # the agent 0.1.20 installed is running its daemon as tailscaled.exe; bring it up the way logon does
    'QPD: ===== 0.1.20 agent running, its daemon under the old name'
    Start-Process -FilePath "$Q\qpsync-agent.exe" -ArgumentList run -WorkingDirectory 'C:\Windows\System32' -WindowStyle Hidden
    Start-Sleep -Seconds 45
    Show-Procs 'on 0.1.20'
    Show-Daemon 'on 0.1.20'

    # exactly what a self-update leaves behind: the new agent binary in place, the old agent gone, the old daemon
    # still running from the old file (update.go's reexec starts the new agent and exits, stopping nothing)
    'QPD: ===== the state a self-update leaves: new agent binary, old daemon still running'
    Get-Process qpsync-agent -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2
    Copy-Item "$E\agent-new\qpsync-agent.exe" "$Q\qpsync-agent.exe" -Force
    Show-Procs 'old daemon alone'
    Start-Process -FilePath "$Q\qpsync-agent.exe" -ArgumentList run -WorkingDirectory 'C:\Windows\System32' -WindowStyle Hidden
    Start-Sleep -Seconds 60
    Show-Daemon 'after the new agent started'
    Show-Procs 'after the new agent started'
}
else {
    # fresh and reinstall: run the background agent the way Task Scheduler does, from C:\Windows\System32
    'QPD: ===== background agent, started in C:\Windows\System32 as Task Scheduler does'
    Start-Process -FilePath "$Q\qpsync-agent.exe" -ArgumentList run -WorkingDirectory 'C:\Windows\System32' -WindowStyle Hidden
    Start-Sleep -Seconds 20
}

# the file that proves the sync direction: it must appear in this circle's bucket
$proof = "qpd-proof-$RUN-$V.txt"
Set-Content -Path (Join-Path $folder.FullName $proof) -Value "Quietport 0.1.21 diagnostic $RUN $V $(Get-Date -Format o)"
"QPD: proof file written: $proof"
Start-Sleep -Seconds 70

Show-Procs 'at the end'
Show-Daemon 'at the end'
Show-Folder 'at the end'
"QPD: System32 copies present: ntoskrnl.exe=$(Test-Path (Join-Path $folder.FullName 'ntoskrnl.exe'))"
'QPD: ===== agent status'
& "$Q\qpsync-agent.exe" status
Show-Log
Stop-Ours
Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.ProcessName -eq 'tailscaled' } |
    ForEach-Object { "QPD: decoy left running at the end: pid=$($_.Id)"; Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue }
'QPD: done'
