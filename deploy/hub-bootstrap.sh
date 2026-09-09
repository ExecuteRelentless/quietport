#!/bin/bash
# Quietport hub, phase 1: OS hardening + component install. Run as root on a fresh Ubuntu 24.04 (arm64 or amd64) VM.
# Idempotent. Phase 2 (configs + services) is hub-configure.sh.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
HEADSCALE_VER=0.29.3
GARAGE_VER=v2.2.0
RCLONE_VER=v1.75.1
ARCH=$(dpkg --print-architecture)   # arm64 | amd64
case "$ARCH" in arm64) GARAGE_ARCH=aarch64-unknown-linux-musl;; amd64) GARAGE_ARCH=x86_64-unknown-linux-musl;; esac

log(){ echo "[$(date +%T)] $*"; }
hostnamectl set-hostname quietport-hub || true
timedatectl set-timezone America/Los_Angeles || true

log "apt"
apt-get update -q
apt-get upgrade -y -q
apt-get install -y -q fail2ban unattended-upgrades git curl unzip jq sqlite3 iptables-persistent ca-certificates

log "unattended-upgrades"
cat > /etc/apt/apt.conf.d/20auto-upgrades <<'X'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
X

log "sshd hardening (NFR-24)"
cat > /etc/ssh/sshd_config.d/99-quietport.conf <<'X'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
PubkeyAuthentication yes
X
systemctl restart ssh

log "fail2ban"
cat > /etc/fail2ban/jail.local <<'X'
[DEFAULT]
backend = systemd
bantime = 1h
findtime = 10m
maxretry = 5
[sshd]
enabled = true
X
systemctl enable --now fail2ban
systemctl restart fail2ban

log "firewall: 22/tcp 443/tcp 41641/udp public, everything else rejected (FR-71)"
# OCI Ubuntu ships an iptables ruleset ending in REJECT; insert ours before it, once.
add_rule(){ iptables -C INPUT "$@" 2>/dev/null || iptables -I INPUT 5 "$@"; }
add_rule -p tcp -m state --state NEW -m tcp --dport 443 -j ACCEPT
add_rule -p udp --dport 41641 -j ACCEPT
add_rule -i tailscale0 -j ACCEPT
netfilter-persistent save >/dev/null

log "tailscale"
if ! command -v tailscale >/dev/null; then curl -fsSL https://tailscale.com/install.sh | sh; fi
systemctl enable tailscaled

log "headscale $HEADSCALE_VER"
if ! command -v headscale >/dev/null || ! headscale version 2>/dev/null | grep -q "$HEADSCALE_VER"; then
  curl -fsSL -o /tmp/headscale.deb "https://github.com/juanfont/headscale/releases/download/v${HEADSCALE_VER}/headscale_${HEADSCALE_VER}_linux_${ARCH}.deb"
  dpkg -i /tmp/headscale.deb
fi

log "garage $GARAGE_VER"
if ! command -v garage >/dev/null || ! garage --version 2>/dev/null | grep -q "${GARAGE_VER#v}"; then
  curl -fsSL -o /usr/local/bin/garage "https://garagehq.deuxfleurs.fr/_releases/${GARAGE_VER}/${GARAGE_ARCH}/garage"
  chmod 755 /usr/local/bin/garage
fi
id -u garage >/dev/null 2>&1 || useradd --system --home /var/lib/garage --shell /usr/sbin/nologin garage
mkdir -p /var/lib/garage/meta /var/lib/garage/data /etc/garage
chown -R garage:garage /var/lib/garage

log "rclone $RCLONE_VER (hub side: backups + canary)"
if ! command -v rclone >/dev/null || ! rclone version 2>/dev/null | head -1 | grep -q "${RCLONE_VER}"; then
  curl -fsSL -o /tmp/rclone.zip "https://github.com/rclone/rclone/releases/download/${RCLONE_VER}/rclone-${RCLONE_VER}-linux-${ARCH}.zip"
  (cd /tmp && unzip -q -o rclone.zip && install -m 755 rclone-${RCLONE_VER}-linux-${ARCH}/rclone /usr/local/bin/rclone)
fi

log "quietport user + dirs"
id -u quietport >/dev/null 2>&1 || useradd --system --home /var/lib/quietport --shell /usr/sbin/nologin quietport
mkdir -p /etc/quietport /var/lib/quietport/{releases,backup,policy} /var/log/quietport
chown -R quietport:quietport /var/lib/quietport /var/log/quietport
chmod 750 /etc/quietport
# qp-hub shells out to headscale and garage CLIs
usermod -aG headscale quietport 2>/dev/null || true
usermod -aG garage quietport 2>/dev/null || true

log "phase 1 done"
tailscale version | head -1; headscale version; garage --version; rclone version | head -1
