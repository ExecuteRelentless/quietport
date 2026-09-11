# Runs as the standard local user qpstd (PsExec, real logon, profile loaded). Installs, puts one proof file in the circle
# folder, then starts the background agent the way Task Scheduler does: in C:\Windows\System32. Up to 0.1.18 the crypt
# remote "C:" was the C drive on Windows, so the folder was bisynced with System32 (2026-09-11). Reports what the folder
# holds after 60 s. Whether the proof file reached the hub is checked by the operator in the circle's bucket.
$E = 'C:\Users\Public\qpe2e'
"CWD: user=$env:USERNAME"
$Q = Join-Path $env:LOCALAPPDATA 'Quietport'
New-Item -ItemType Directory -Force $Q | Out-Null
Copy-Item "$E\bundle\*" $Q -Force
Copy-Item "$E\agent\qpsync-agent.exe" "$Q\qpsync-agent.exe" -Force
# the real installer runs Install inside Quietport.exe (Install kills qpsync-agent.exe by name)
$I = Join-Path $E 'installer'
New-Item -ItemType Directory -Force $I | Out-Null
Copy-Item "$E\agent\qpsync-agent.exe" "$I\Quietport.exe" -Force
$code = (Get-Content "$E\code.txt" -Raw).Trim()

'CWD: ===== install'
& "$I\Quietport.exe" install --code $code --payload "$E\payload.json"
"CWD: install exit=$LASTEXITCODE"
Get-Process qpsync-agent, rclone, tailscaled -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2
$folder = Get-ChildItem (Join-Path $env:USERPROFILE 'QPSync') -Directory -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $folder) { 'CWD: no circle folder after install'; 'CWD: done'; exit }
$proof = (Get-Content "$E\proof.txt" -Raw).Trim()
Set-Content -Path (Join-Path $folder.FullName $proof) -Value "Quietport CI proof $(Get-Date -Format o)"
"CWD: circle folder $($folder.Name), proof file $proof written"

'CWD: ===== background agent started in C:\Windows\System32, as Task Scheduler does'
Start-Process -FilePath "$Q\qpsync-agent.exe" -ArgumentList run -WorkingDirectory 'C:\Windows\System32' -WindowStyle Hidden
Start-Sleep -Seconds 60
Get-Process qpsync-agent, rclone, tailscaled -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
$files = @(Get-ChildItem $folder.FullName -Recurse -File -Force -ErrorAction SilentlyContinue | Where-Object { $_.FullName -notlike '*\.qp-versions\*' })
$mb = [math]::Round((($files | Measure-Object Length -Sum).Sum) / 1MB, 1)
"CWD: circle folder after 60 s: $($files.Count) files, $mb MB"
"CWD: System32 copies present: ntoskrnl.exe=$(Test-Path (Join-Path $folder.FullName 'ntoskrnl.exe')), .dll files=$(@($files | Where-Object Extension -eq '.dll').Count)"
'CWD: ===== agent status'
& "$Q\qpsync-agent.exe" status
'CWD: ===== agent.log (agent lines, tailscaled lines left out)'
Get-Content "$Q\logs\agent.log" -ErrorAction SilentlyContinue | Where-Object { $_ -notmatch 'tailscaled:' } | ForEach-Object { $_ -replace '(?i)(auth[-_ ]?key[=:" ]+)[^\s",]+', '$1[masked]' }
'CWD: done'
