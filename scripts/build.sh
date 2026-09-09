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
LD="-s -w -X main.Version=$V -X quietport.app/quietport/internal/agent.Version=$V -X quietport.app/quietport/internal/agent.ReleasePubKey=$PUB"
mkdir -p "$OUT"
cd "$R"

build(){ # os arch out
  local ext=""; [ "$1" = windows ] && ext=.exe
  CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "$LD" -o "$3/qpsync-agent$ext" ./cmd/qpsync-agent
  CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "$LD" -o "$3/qpctl$ext" ./cmd/qpctl
}

echo "== hub (linux/arm64 + amd64)"
for a in arm64 amd64; do
  mkdir -p "$OUT/hub-linux-$a"
  CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "$LD" -o "$OUT/hub-linux-$a/qp-hub" ./cmd/qp-hub
  CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "$LD" -o "$OUT/hub-linux-$a/qpctl" ./cmd/qpctl
  cp -R deploy "$OUT/hub-linux-$a/"
  (cd "$OUT" && tar czf "quietport-hub-linux-$a.tar.gz" "hub-linux-$a")
done

echo "== client bundles"
for t in "darwin arm64 osx-arm64" "darwin amd64 osx-amd64" "windows amd64 windows-amd64"; do
  os=$(echo $t | cut -d' ' -f1); arch=$(echo $t | cut -d' ' -f2); rcl=$(echo $t | cut -d' ' -f3)
  D="$OUT/client-$os-$arch"; rm -rf "$D"; mkdir -p "$D"
  build $os $arch "$D"
  ext=""; [ "$os" = windows ] && ext=.exe
  cp "$VB/rclone/rclone-$RCLONE_VER-$rcl/rclone$ext" "$D/"
  cp "$VB/ts-$os-$arch/tailscaled$ext" "$VB/ts-$os-$arch/tailscale$ext" "$D/"
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
