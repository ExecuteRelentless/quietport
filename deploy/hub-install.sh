#!/bin/bash
# Install/upgrade the hub binary, backup jobs and cron from an unpacked hub-linux-<arch> directory. Run as root on the hub.
set -euo pipefail
D=$(cd "$(dirname "$0")" && pwd)
install -m 755 "$D/../qp-hub" /usr/local/bin/qp-hub
install -m 755 "$D/../qpctl" /usr/local/bin/qpctl
mkdir -p /usr/local/lib/quietport
install -m 755 "$D/backup.sh" "$D/backup-verify.sh" /usr/local/lib/quietport/
install -m 644 "$D/cron.d-quietport" /etc/cron.d/quietport
install -m 644 "$D/systemd/qp-hub.service" /etc/systemd/system/qp-hub.service
systemctl daemon-reload
if systemctl is-enabled qp-hub >/dev/null 2>&1; then systemctl restart qp-hub; fi
echo "installed qp-hub $(/usr/local/bin/qp-hub version)"
