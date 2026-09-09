#!/bin/bash
# On the hub: move uploaded bundles from /tmp into the releases dir and register them (agents self-update from this).
# Usage: sudo bash publish-release.sh <version>
set -euo pipefail
V="${1:?version}"
mv /tmp/quietport-*-"$V".* /var/lib/quietport/releases/ 2>/dev/null || true
mv /tmp/quietport-hub-linux-*.tar.gz /tmp/quietport-operator-darwin-*.tar.gz /tmp/SHA256SUMS.signed /var/lib/quietport/releases/ 2>/dev/null || true
chown quietport:quietport /var/lib/quietport/releases/*
ENV=$(cat /etc/quietport/hub.env | xargs)
while read -r sha file sig; do
  case "$file" in
    *darwin-arm64-"$V"*) os=darwin; arch=arm64;;
    *darwin-amd64-"$V"*) os=darwin; arch=amd64;;
    *windows-amd64-"$V"*) os=windows; arch=amd64;;
    *) continue;;
  esac
  sudo -u quietport env $ENV /usr/local/bin/qp-hub release add --os $os --arch $arch --version "$V" --file "$file" --sig "$sig"
done < /var/lib/quietport/releases/SHA256SUMS.signed
