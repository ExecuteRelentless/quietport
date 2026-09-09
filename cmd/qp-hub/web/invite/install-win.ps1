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
& "$App\qpsync-agent.exe" install --code $Code --payload "$App\payload.json"
exit $LASTEXITCODE
