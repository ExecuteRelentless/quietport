#!/bin/bash
# On the hub: move uploaded bundles from /tmp into the releases dir and register them (agents self-update from this).
# Usage: sudo bash publish-release.sh <version>
set -euo pipefail
V="${1:?version}"
mv /tmp/quietport-*-"$V".* /var/lib/quietport/releases/ 2>/dev/null || true
mv /tmp/quietport-hub-linux-*.tar.gz /tmp/quietport-operator-darwin-*.tar.gz /tmp/SHA256SUMS.signed /var/lib/quietport/releases/ 2>/dev/null || true
if [ -f /tmp/quietport-installer-darwin.tar.gz ]; then
  rm -rf /var/lib/quietport/releases/installer && mkdir -p /var/lib/quietport/releases/installer
  tar xzf /tmp/quietport-installer-darwin.tar.gz -C /var/lib/quietport/releases/installer 2>/dev/null
  mv /tmp/quietport-installer-darwin.tar.gz /var/lib/quietport/releases/
fi
[ -f /tmp/quietport-installer-windows-amd64.exe ] && mv /tmp/quietport-installer-windows-amd64.exe /var/lib/quietport/releases/
[ -f /tmp/quietport-installer-darwin.dmg ] && mv /tmp/quietport-installer-darwin.dmg /var/lib/quietport/releases/
for a in amd64 arm64; do [ -f /tmp/quietport-installer-linux-$a ] && mv /tmp/quietport-installer-linux-$a /var/lib/quietport/releases/; done
chown -R quietport:quietport /var/lib/quietport/releases
ENV=$(cat /etc/quietport/hub.env | xargs)
while read -r sha file sig; do
  case "$file" in
    *darwin-arm64-"$V"*) os=darwin; arch=arm64;;
    *darwin-amd64-"$V"*) os=darwin; arch=amd64;;
    *windows-amd64-"$V"*) os=windows; arch=amd64;;
    *linux-amd64-"$V"*) os=linux; arch=amd64;;
    *linux-arm64-"$V"*) os=linux; arch=arm64;;
    *) continue;;
  esac
  sudo -u quietport env $ENV /usr/local/bin/qp-hub release add --os $os --arch $arch --version "$V" --file "$file" --sig "$sig"
done < /var/lib/quietport/releases/SHA256SUMS.signed
