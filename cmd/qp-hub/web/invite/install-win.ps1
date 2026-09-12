# Quietport installer for Windows. Personalised for one invitation. Runs entirely as the current user.
$ErrorActionPreference = 'Stop'; $ProgressPreference = 'SilentlyContinue'
$QHost = '__HOST__'; $Code = '__CODE__'; $Support = '__SUPPORT__'; $Operator = '__OPERATOR__'
$App = Join-Path $env:LOCALAPPDATA 'Quietport'
function Fail($m) { Write-Host "Quietport could not be installed: $m Please contact $Operator ($Support)."; exit 1 }
try { New-Item -ItemType Directory -Force -Path $App | Out-Null } catch { Fail 'the application folder could not be created.' }
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
try { Invoke-WebRequest -UseBasicParsing "https://$QHost/dl/quietport-windows-amd64.zip" -OutFile "$App\bundle.zip" } catch { Fail 'the download did not complete.' }
try { Expand-Archive -Force "$App\bundle.zip" $App; Remove-Item "$App\bundle.zip" } catch { Fail 'the download was damaged.' }
try { Invoke-WebRequest -UseBasicParsing "https://$QHost/j/$Code/payload" -OutFile "$App\payload.json" } catch { Fail 'this invitation link is no longer valid.' }
# piped on purpose: the agent is a GUI-subsystem program (docs/adr/0017) and PowerShell does not wait for one of
# those unless its output is going somewhere, which would end this script before the install had finished
& "$App\qpsync-agent.exe" install --code $Code --payload "$App\payload.json" | Out-Host
exit $LASTEXITCODE
