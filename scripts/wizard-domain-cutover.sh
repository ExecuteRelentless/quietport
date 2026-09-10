#!/bin/bash
# Walks the operator through moving the hub from a temporary hostname (e.g. 165-1-66-170.sslip.io) to a real domain.
# Steps only a human can do are described and gated; the rest runs over ssh. Existing devices keep working on the
# old hostname (it stays in the certificate and still resolves), so nobody has to reinstall.
#
# Usage: scripts/wizard-domain-cutover.sh <new domain> <ssh host of the hub> <release version to rebuild>
#   e.g. scripts/wizard-domain-cutover.sh quietport.app quietport-hub 0.1.15
set -euo pipefail
NEW="${1:?new domain, e.g. quietport.app}"
HUB="${2:?ssh host of the hub}"
VER="${3:?version to build the installers as, e.g. 0.1.15}"
R=$(cd "$(dirname "$0")/.." && pwd)

bold() { printf '\n\033[1m%s\033[0m\n' "$*"; }
gate() { read -r -p "$1 [press Enter when done, or Ctrl-C to stop] " _; }
OLD=$(ssh "$HUB" 'sudo grep "^QP_HOST=" /etc/quietport/hub.env | cut -d= -f2')
IP=$(ssh "$HUB" 'curl -s -4 https://ifconfig.me || hostname -I | cut -d" " -f1')
bold "Stage 1 of 5: the domain"
echo "Hub is $HUB at $IP, currently serving $OLD. Target: $NEW (site + invites) and hub.$NEW (mesh)."
echo "Register $NEW at the registrar of your choice (Cloudflare Registrar is at cost, about \$14/year for .app)."
gate "Registered?"

bold "Stage 2 of 5: DNS (do this in the registrar's DNS panel)"
cat <<EOF
  A     $NEW        -> $IP      (proxy OFF / DNS only; the invite links and downloads come from the hub itself)
  A     hub.$NEW    -> $IP      (proxy OFF / DNS only; WireGuard control traffic cannot go through a proxy)
Optional: AAAA records if the VM has IPv6. No CAA record is needed; Let's Encrypt issues on first request.
EOF
gate "Both A records saved?"
echo -n "Waiting for DNS"
for _ in $(seq 1 60); do
  if [ "$(dig +short A "$NEW" | head -1)" = "$IP" ] && [ "$(dig +short A "hub.$NEW" | head -1)" = "$IP" ]; then echo " ok"; break; fi
  echo -n "."; sleep 10
done
[ "$(dig +short A "$NEW" | head -1)" = "$IP" ] || { echo "DNS for $NEW does not point at $IP yet; run the wizard again later."; exit 1; }

bold "Stage 3 of 5: hub configuration (runs over ssh)"
echo "hub.env: QP_HOST=$NEW, QP_EXTRA_HOSTS=hub.$NEW,$OLD ; headscale server_url -> https://hub.$NEW ; restart both."
gate "Apply?"
ssh "$HUB" "sudo sed -i 's/^QP_HOST=.*/QP_HOST=$NEW/; s/^QP_EXTRA_HOSTS=.*/QP_EXTRA_HOSTS=hub.$NEW,$OLD/' /etc/quietport/hub.env && \
  (sudo grep -q '^QP_EXTRA_HOSTS=' /etc/quietport/hub.env || echo 'QP_EXTRA_HOSTS=hub.$NEW,$OLD' | sudo tee -a /etc/quietport/hub.env >/dev/null) && \
  sudo sed -i 's#^server_url: .*#server_url: https://hub.$NEW#' /etc/headscale/config.yaml && \
  sudo systemctl restart headscale qp-hub && sleep 3 && systemctl is-active headscale qp-hub"
echo -n "Waiting for the certificate"
for _ in $(seq 1 30); do
  if curl -s -o /dev/null -w '%{http_code}' "https://$NEW/" | grep -q 200; then echo " ok"; break; fi
  echo -n "."; sleep 5
done
curl -s -o /dev/null -w "site https://$NEW -> %{http_code}\n" "https://$NEW/"
curl -s -o /dev/null -w "old host https://$OLD -> %{http_code} (must stay 200 for existing devices)\n" "https://$OLD/"

bold "Stage 4 of 5: installers with the new default host"
echo "Builds $VER with DefaultHost=$NEW, notarizes, and publishes. Needs NOTARY_KEY, NOTARY_KEY_ID, NOTARY_ISSUER in the environment."
gate "Build and publish?"
( cd "$R" && export GOFLAGS=-mod=mod && scripts/build.sh "$VER" && scripts/build-installer.sh "$VER" "$NEW" )
scp -q "$R"/dist/"$VER"/* "$HUB":/tmp/
ssh "$HUB" "cd /tmp && rm -rf hubup && mkdir hubup && tar xzf quietport-hub-linux-\$(uname -m | sed 's/aarch64/arm64/;s/x86_64/amd64/').tar.gz -C hubup && sudo bash hubup/hub-linux-*/deploy/hub-install.sh && sudo bash hubup/hub-linux-*/deploy/publish-release.sh $VER"

bold "Stage 5 of 5: check"
curl -s -o /dev/null -w "DMG https://$NEW/dl/Quietport.dmg -> %{http_code}\n" "https://$NEW/dl/Quietport.dmg"
curl -s -o /dev/null -w "EXE https://$NEW/dl/Quietport.exe -> %{http_code}\n" "https://$NEW/dl/Quietport.exe"
cat <<EOF
Done. New invite links use https://$NEW/j/<code>. Devices already enrolled keep using $OLD and update themselves
to $VER on their next heartbeat. Update the GitHub variable HUB_HOST to $NEW so CI builds carry the new default too.
EOF
