#!/bin/bash
# Quietport hub, phase 2: write configs, start headscale + tailscaled, join the hub to its own mesh, start garage.
# Usage: sudo bash hub-configure.sh <public hostname>   (run after hub-bootstrap.sh and after /usr/local/bin/qp-hub is installed)
set -euo pipefail
HOST="${1:?public hostname}"
D=$(cd "$(dirname "$0")" && pwd)
log(){ echo "[$(date +%T)] $*"; }

log "headscale config"
sed "s#__HUB_HOST__#$HOST#" "$D/headscale/config.yaml" > /etc/headscale/config.yaml
mkdir -p /var/lib/headscale /var/run/headscale
chown -R headscale:headscale /var/lib/headscale /var/run/headscale /etc/headscale
systemctl enable headscale
systemctl restart headscale
for i in $(seq 1 30); do [ -S /run/headscale/headscale.sock ] && break; sleep 1; done
[ -S /run/headscale/headscale.sock ] || { journalctl -u headscale -n 30 --no-pager; exit 1; }

log "headscale users + policy"
headscale users list -o json 2>/dev/null | grep -q '"name": *"hub"' || headscale users create hub
cp "$D/headscale/policy.hujson" /var/lib/quietport/policy/policy.hujson
chown -R quietport:quietport /var/lib/quietport/policy
(cd /var/lib/quietport/policy && { [ -d .git ] || sudo -u quietport git init -q -b main; } && sudo -u quietport git add policy.hujson && sudo -u quietport git -c user.name=qp-hub -c user.email=hub@quietport.app commit -q -m "initial policy" 2>/dev/null || true)
headscale policy set -f /var/lib/quietport/policy/policy.hujson

log "join the hub node to its own mesh (tag:hub) over loopback"
HUBUID=$(headscale users list -o json | python3 -c "import sys,json;print([u['id'] for u in json.load(sys.stdin) if u['name']=='hub'][0])")
if ! tailscale status >/dev/null 2>&1 || ! tailscale status 2>/dev/null | grep -q quietport-hub; then
  KEY=$(headscale preauthkeys create --user "$HUBUID" --expiration 1h --tags tag:hub -o json | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
  tailscale up --reset --login-server http://127.0.0.1:8080 --authkey "$KEY" --accept-dns=false --hostname quietport-hub
fi
for i in $(seq 1 30); do TSIP=$(tailscale ip -4 2>/dev/null || true); [ -n "$TSIP" ] && break; sleep 2; done
[ -n "$TSIP" ] || { echo "no tailnet ip"; exit 1; }
log "hub tailnet ip $TSIP"
echo "$TSIP" > /etc/quietport/tailnet-ip

log "garage config"
if [ ! -f /etc/garage/garage.toml ]; then
  RPC=$(openssl rand -hex 32); ADM=$(openssl rand -hex 32); MET=$(openssl rand -hex 32)
  sed -e "s#__RPC_SECRET__#$RPC#" -e "s#__TAILNET_IP__#$TSIP#" -e "s#__ADMIN_TOKEN__#$ADM#" -e "s#__METRICS_TOKEN__#$MET#" "$D/garage/garage.toml" > /etc/garage/garage.toml
  chown root:garage /etc/garage/garage.toml; chmod 640 /etc/garage/garage.toml
fi
cp "$D/systemd/garage.service" /etc/systemd/system/garage.service
systemctl daemon-reload
systemctl enable --now garage
sleep 3
# single-node layout, once
if ! garage -c /etc/garage/garage.toml layout show 2>/dev/null | grep -q "dc1"; then
  NODEID=$(garage -c /etc/garage/garage.toml node id -q 2>/dev/null | cut -d@ -f1)
  garage -c /etc/garage/garage.toml layout assign -z dc1 -c 80G "$NODEID"
  garage -c /etc/garage/garage.toml layout apply --version 1
fi
garage -c /etc/garage/garage.toml status

log "qp-hub env"
[ -f /etc/quietport/hub.env ] || cat > /etc/quietport/hub.env <<X
QP_HOST=$HOST
QP_TAILNET_IP=$TSIP
QP_DB=/var/lib/quietport/hub.db
QP_RELEASES=/var/lib/quietport/releases
QP_POLICY_DIR=/var/lib/quietport/policy
QP_GARAGE_CONFIG=/etc/garage/garage.toml
QP_ACME_DIR=/var/lib/quietport/acme
QP_HEADSCALE_URL=http://127.0.0.1:8080
QP_OPERATOR_NAME=Quietport
QP_SUPPORT_CONTACT=
X
chown root:quietport /etc/quietport/hub.env; chmod 640 /etc/quietport/hub.env
mkdir -p /var/lib/quietport/acme; chown quietport:quietport /var/lib/quietport/acme
# quietport user needs the headscale socket + garage config
usermod -aG headscale quietport; usermod -aG garage quietport
chmod 750 /var/run/headscale || true
cp "$D/systemd/qp-hub.service" /etc/systemd/system/qp-hub.service
systemctl daemon-reload
if [ -x /usr/local/bin/qp-hub ]; then systemctl enable --now qp-hub; sleep 2; systemctl is-active qp-hub; fi
log "phase 2 done"
