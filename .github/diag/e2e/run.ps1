# Runs as the standard local user qpstd (PsExec, real logon, profile loaded). Does what the Windows installer does
# after its download step: unpack the client bundle into %LOCALAPPDATA%\Quietport, then `qpsync-agent install`.
# Then, without touching the daemon, checks the hub is still reachable through the SOCKS proxy 40 s later.
$E = 'C:\Users\Public\qpe2e'
"E2E: user=$env:USERNAME localappdata=$env:LOCALAPPDATA process_arch=$env:PROCESSOR_ARCHITECTURE"
whoami /groups | Select-String 'Administrators|Mandatory Label'
$Q = Join-Path $env:LOCALAPPDATA 'Quietport'
New-Item -ItemType Directory -Force $Q | Out-Null
Copy-Item "$E\bundle\*" $Q -Force
$code = (Get-Content "$E\code.txt" -Raw).Trim()

'E2E: ===== install'
$sw = [Diagnostics.Stopwatch]::StartNew()
& "$Q\qpsync-agent.exe" install --code $code --payload "$E\payload.json"
"E2E: install exit=$LASTEXITCODE after $([int]$sw.Elapsed.TotalSeconds) s"

'E2E: ===== processes right after install'
Get-Process qpsync-agent, tailscaled -ErrorAction SilentlyContinue | Format-Table Id, ProcessName, StartTime -AutoSize | Out-String
if (-not (Get-Process qpsync-agent -ErrorAction SilentlyContinue)) {
  'E2E: no background agent (a PsExec session cannot run the logon task); starting "qpsync-agent run" as the Run key would'
  Start-Process -FilePath "$Q\qpsync-agent.exe" -ArgumentList run -WindowStyle Hidden
}
Start-Sleep -Seconds 40

'E2E: ===== 40 s later, no CLI call from this script yet: hub API through the SOCKS proxy'
$port = ''
if ((Get-Content "$Q\config.json" -Raw -ErrorAction SilentlyContinue) -match '"socks_?port"\s*:\s*(\d+)') { $port = $Matches[1] }
if ($port) {
  & curl.exe -sS -o NUL -w "E2E: socks5 127.0.0.1:$port -> http://100.64.0.1:8443/ HTTP %{http_code}`n" --max-time 15 --socks5-hostname "127.0.0.1:$port" http://100.64.0.1:8443/
} else { 'E2E: no socks port in config.json' }

'E2E: ===== tailscale status'
& "$Q\tailscale.exe" --socket=\\.\pipe\quietport-tailscaled status
'E2E: ===== agent status'
& "$Q\qpsync-agent.exe" status
'E2E: ===== agent.log'
Get-Content "$Q\logs\agent.log" -ErrorAction SilentlyContinue | ForEach-Object { $_ -replace '(?i)(auth[-_ ]?key[=:" ]+)[^\s",]+', '$1[masked]' }
'E2E: done'
