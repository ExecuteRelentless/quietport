#!/bin/bash
# Build every Quietport binary and the per-platform client bundles. Run on the Mac (darwin cgo not needed: pure Go).
# Usage: scripts/build.sh <version>      → dist/<version>/
set -euo pipefail
V="${1:?version}"
R=$(cd "$(dirname "$0")/.." && pwd)
VB="$R/../vendor-bins"
OUT="$R/dist/$V"
RCLONE_VER=v1.75.1
KEYS="$R/../release-keys"
PUB=$(cat "$KEYS/release.pub" 2>/dev/null || echo "")
LDW="-X main.Version=$V -X quietport.app/quietport/internal/agent.Version=$V -X quietport.app/quietport/internal/agent.ReleasePubKey=$PUB"
LD="-s -w -X main.Version=$V -X quietport.app/quietport/internal/agent.Version=$V -X quietport.app/quietport/internal/agent.ReleasePubKey=$PUB"
mkdir -p "$OUT"
cd "$R"

build(){ # os arch out
  local ext="" flags="$LD"; [ "$1" = windows ] && { ext=.exe; flags="$LDW"; }
  CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "$flags" -o "$3/qpsync-agent$ext" ./cmd/qpsync-agent
  CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "$flags" -o "$3/qpctl$ext" ./cmd/qpctl
}

echo "== windows resources (icon, version info, manifest)"
export PATH="$PATH:$HOME/go/bin"
for d in cmd/qp-installer cmd/qpsync-agent; do
  sed -E "s/\"(file_version|product_version)\": \"[0-9.]+\"/\"\1\": \"$V.0\"/g; s/\"(FileVersion|ProductVersion)\": \"[0-9.]+\"/\"\1\": \"$V\"/g; s/\"version\": \"[0-9.]+\"/\"version\": \"$V.0\"/" "$d/winres/winres.json" > "$d/winres/.gen.json"
  (cd "$d" && go-winres make --arch amd64 --in winres/.gen.json >/dev/null && rm -f winres/.gen.json)
done

echo "== hub (linux/arm64 + amd64)"
for a in arm64 amd64; do
  mkdir -p "$OUT/hub-linux-$a"
  CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "$LD" -o "$OUT/hub-linux-$a/qp-hub" ./cmd/qp-hub
  CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "$LD" -o "$OUT/hub-linux-$a/qpctl" ./cmd/qpctl
  cp -R deploy "$OUT/hub-linux-$a/"
  mkdir -p "$OUT/hub-linux-$a/docs"; cp README.md LICENSE "$OUT/hub-linux-$a/"; cp docs/RUNBOOK.md "$OUT/hub-linux-$a/docs/"
  (cd "$OUT" && COPYFILE_DISABLE=1 tar czf "quietport-hub-linux-$a.tar.gz" "hub-linux-$a")
done

echo "== client bundles"
for t in "darwin arm64 osx-arm64" "darwin amd64 osx-amd64" "windows amd64 windows-amd64" "linux amd64 linux-amd64" "linux arm64 linux-arm64"; do
  os=$(echo $t | cut -d' ' -f1); arch=$(echo $t | cut -d' ' -f2); rcl=$(echo $t | cut -d' ' -f3)
  D="$OUT/client-$os-$arch"; rm -rf "$D"; mkdir -p "$D"
  build $os $arch "$D"
  ext=""; [ "$os" = windows ] && ext=.exe
  cp "$VB/rclone/rclone-$RCLONE_VER-$rcl/rclone$ext" "$D/"
  cp "$VB/ts-$os-$arch/tailscale$ext" "$D/"
  # the Windows Firewall prompt names the file that listens, so there the daemon carries the product's name (docs/adr/0015)
  if [ "$os" = windows ]; then cp "$VB/ts-$os-$arch/tailscaled.exe" "$D/Quietport Network.exe"; else cp "$VB/ts-$os-$arch/tailscaled" "$D/"; fi
  if [ "$os" = darwin ]; then cp "$R/installers/mac/qp-sidebar" "$D/"; fi
  chmod 755 "$D"/*
  echo "$V" > "$D/VERSION"
  if [ "$os" = windows ]; then
    (cd "$D" && rm -f "../quietport-$os-$arch-$V.zip" && zip -q -j "../quietport-$os-$arch-$V.zip" ./*)
  else
    (cd "$D" && COPYFILE_DISABLE=1 tar czf "../quietport-$os-$arch-$V.tar.gz" ./*)
  fi
done

echo "== operator kit (qpctl + rclone) for the Mac"
mkdir -p "$OUT/operator-darwin-arm64" "$OUT/operator-darwin-amd64"
cp "$OUT/client-darwin-arm64/qpctl" "$OUT/client-darwin-arm64/rclone" "$OUT/operator-darwin-arm64/"
cp "$OUT/client-darwin-amd64/qpctl" "$OUT/client-darwin-amd64/rclone" "$OUT/operator-darwin-amd64/"
for d in operator-darwin-arm64 operator-darwin-amd64; do mkdir -p "$OUT/$d/docs"; cp README.md LICENSE "$OUT/$d/"; cp docs/RUNBOOK.md "$OUT/$d/docs/"; done
(cd "$OUT" && COPYFILE_DISABLE=1 tar czf quietport-operator-darwin-arm64.tar.gz operator-darwin-arm64 && COPYFILE_DISABLE=1 tar czf quietport-operator-darwin-amd64.tar.gz operator-darwin-amd64)

echo "== signatures (NFR-40)"
if [ -f "$KEYS/release.key" ]; then
  rm -f "$OUT/SHA256SUMS.signed"
  for f in "$OUT"/quietport-*.tar.gz "$OUT"/quietport-*.zip; do
    sha=$(shasum -a 256 "$f" | cut -d' ' -f1)
    sig=$(go run ./scripts/sign -key "$KEYS/release.key" -msg "$sha")
    echo "$sha  $(basename "$f")  $sig" >> "$OUT/SHA256SUMS.signed"
  done
  cat "$OUT/SHA256SUMS.signed"
else
  echo "no release key at $KEYS/release.key; bundles unsigned (agents will refuse self-update)"
fi
ls -la "$OUT"
