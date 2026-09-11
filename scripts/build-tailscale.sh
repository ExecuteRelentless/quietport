#!/bin/bash
# Build tailscaled and the tailscale CLI from unpatched upstream source for one platform, with Quietport's flags.
# Usage: scripts/build-tailscale.sh <os> <arch> <outdir>       (env: TS_VERSION, TSBUILD_DIR)
# Tailscale is not a dependency of this repo; it is built from its own module in TSBUILD_DIR (a scratch dir by default).
#
# Windows (docs/adr/0010): Quietport runs tailscaled unelevated, as the member, never as a service. Stock tailscaled
# assumes it is a service, in two places, so the Windows daemon is built with:
#   -tags ts_omit_syspolicy      no Windows policy store; its user-scope Group Policy lock is refused for a standard user
#   -X tailscale.com/safesocket.windowsSDDL=...   a control pipe with no owner; stock names BUILTIN\Administrators as
#                                owner, which a non-elevated process cannot assign (ERROR_INVALID_OWNER), so it exits
# Other platforms are stock.
set -euo pipefail
OS="${1:?os}"; ARCH="${2:?arch}"; OUT="${3:?outdir}"
TS_VERSION="${TS_VERSION:-v1.102.3}"
OUT=$(mkdir -p "$OUT" && cd "$OUT" && pwd)
D="${TSBUILD_DIR:-$(mktemp -d)}"; mkdir -p "$D"; cd "$D"
[ -f go.mod ] || go mod init tsbuild >/dev/null 2>&1
export GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=$OS GOARCH=$ARCH
go get "tailscale.com@$TS_VERSION" >/dev/null 2>&1
ext=""; [ "$OS" = windows ] && ext=.exe
DAEMON_TAGS=""; DAEMON_LD="-s -w"
if [ "$OS" = windows ]; then
  DAEMON_TAGS="ts_omit_syspolicy"
  DAEMON_LD="$DAEMON_LD -X tailscale.com/safesocket.windowsSDDL=D:PAI(A;OICI;GWGR;;;BU)(A;OICI;GWGR;;;SY)"
fi
go build -trimpath ${DAEMON_TAGS:+-tags "$DAEMON_TAGS"} -ldflags "$DAEMON_LD" -o "$OUT/tailscaled$ext" tailscale.com/cmd/tailscaled
go build -trimpath -ldflags "-s -w" -o "$OUT/tailscale$ext" tailscale.com/cmd/tailscale
echo "tailscale $TS_VERSION for $OS/$ARCH -> $OUT (daemon tags: ${DAEMON_TAGS:-none})"
ls -l "$OUT/tailscaled$ext" "$OUT/tailscale$ext"
