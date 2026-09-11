# Runs as the standard local user qpstd (PsExec, real logon, profile loaded). Reproduces a member who installs again
# on an account where an earlier install left tailscaled's state behind (2026-09-11: a failed 0.1.16 attempt, then
# 0.1.17 registered under the old node key and the hub dropped its traffic). Installs once with the published client,
# then again with a new invite using this job's agent, then checks the hub answers through the SOCKS proxy.
$E = 'C:\Users\Public\qpe2e'
"RI: user=$env:USERNAME process_arch=$env:PROCESSOR_ARCHITECTURE"
$Q = Join-Path $env:LOCALAPPDATA 'Quietport'
New-Item -ItemType Directory -Force $Q | Out-Null
Copy-Item "$E\bundle\*" $Q -Force
$code1 = (Get-Content "$E\code1.txt" -Raw).Trim()
$code2 = (Get-Content "$E\code2.txt" -Raw).Trim()

'RI: ===== first install (published agent)'
& "$Q\qpsync-agent.exe" install --code $code1 --payload "$E\payload1.json"
"RI: first install exit=$LASTEXITCODE"
Get-Process qpsync-agent, tailscaled -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2
"RI: tailscaled state left behind: $(Test-Path "$Q\ts\tailscaled.state")"

'RI: ===== second install, new invite (this job''s agent)'
Copy-Item "$E\agent2\qpsync-agent.exe" "$Q\qpsync-agent.exe" -Force
$sw = [Diagnostics.Stopwatch]::StartNew()
& "$Q\qpsync-agent.exe" install --code $code2 --payload "$E\payload2.json"
"RI: second install exit=$LASTEXITCODE after $([int]$sw.Elapsed.TotalSeconds) s"
if (-not (Get-Process qpsync-agent -ErrorAction SilentlyContinue)) {
  'RI: no background agent (a PsExec session cannot run the logon task); starting "qpsync-agent run" as the Run key would'
  Start-Process -FilePath "$Q\qpsync-agent.exe" -ArgumentList run -WindowStyle Hidden
}
Start-Sleep -Seconds 40

'RI: ===== 40 s later: hub API through the SOCKS proxy'
$port = ''
if ((Get-Content "$Q\config.json" -Raw -ErrorAction SilentlyContinue) -match '"socks_?port"\s*:\s*(\d+)') { $port = $Matches[1] }
if ($port) {
  & curl.exe -sS -o NUL -w "RI: socks5 127.0.0.1:$port -> http://100.64.0.1:8443/ HTTP %{http_code}`n" --max-time 15 --socks5-hostname "127.0.0.1:$port" http://100.64.0.1:8443/
} else { 'RI: no socks port in config.json' }
'RI: ===== agent status'
& "$Q\qpsync-agent.exe" status
'RI: ===== node keys each install registered with (RegisterReq lines)'
Get-Content "$Q\logs\agent.log" -ErrorAction SilentlyContinue | Select-String -Pattern 'install [0-9.]+ on|Generating a new nodekey|RegisterReq: onode|install:' | ForEach-Object { $_.Line }
'RI: ===== agent.log'
Get-Content "$Q\logs\agent.log" -ErrorAction SilentlyContinue | ForEach-Object { $_ -replace '(?i)(auth[-_ ]?key[=:" ]+)[^\s",]+', '$1[masked]' }
'RI: done'
