#!/bin/bash
# Weekly backup integrity check (FR-75): restore the canary from the off-host copy and compare its hash.
set -uo pipefail
set -a; . /etc/quietport/backup.env; set +a
STATE=/var/lib/quietport/backup
export RCLONE_CONFIG_CRYPT_TYPE=crypt RCLONE_CONFIG_CRYPT_REMOTE="off:${BACKUP_BUCKET}/hub" RCLONE_CONFIG_CRYPT_PASSWORD="$BACKUP_CRYPT_PASSWORD" RCLONE_CONFIG_CRYPT_PASSWORD2="$BACKUP_CRYPT_SALT"
GOT=$(rclone cat crypt:canary/canary.txt 2>/dev/null | sha256sum | cut -d' ' -f1)
WANT=$(cat "$STATE/canary.sha256" 2>/dev/null)
RES=failed; [ -n "$GOT" ] && [ "$GOT" = "$WANT" ] && RES=ok
# also prove the hub sqlite restores and opens
rclone copyto crypt:state/hub/hub.db "$STATE/restore-test.db" -q 2>/dev/null && sqlite3 "$STATE/restore-test.db" "select count(*) from person" >/dev/null 2>&1 || RES=failed
rm -f "$STATE/restore-test.db"
printf '{"checked":"%s","result":"%s","canary_sha256":"%s"}\n' "$(date -Iseconds)" "$RES" "$GOT" > "$STATE/canary.json"
chown quietport:quietport "$STATE/canary.json" 2>/dev/null || true
echo "canary $RES"
