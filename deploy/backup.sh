#!/bin/bash
# Nightly hub backup (FR-74): Garage meta snapshot + data, Headscale DB + keys, hub SQLite -> encrypted, off-host via rclone.
# Config: /etc/quietport/backup.env with RCLONE_CONFIG_* for a remote named `off` (S3-compatible) and BACKUP_CRYPT_PASSWORD/SALT.
set -uo pipefail
ENV=/etc/quietport/backup.env
[ -f "$ENV" ] || { echo "no $ENV"; exit 1; }
set -a; . "$ENV"; set +a
STAGE=/var/lib/quietport/backup/stage
STATE=/var/lib/quietport/backup
mkdir -p "$STAGE" "$STATE"
START=$(date -Iseconds)
RESULT=ok; DETAIL=""
{
  rm -rf "$STAGE"/*
  mkdir -p "$STAGE/headscale" "$STAGE/hub"
  # headscale: consistent sqlite copy + noise key
  sqlite3 /var/lib/headscale/db.sqlite ".backup '$STAGE/headscale/db.sqlite'"
  cp /var/lib/headscale/noise_private.key "$STAGE/headscale/"
  cp /etc/headscale/config.yaml "$STAGE/headscale/"
  # hub state + policy repo + garage config (contains admin token; encrypted anyway)
  sqlite3 /var/lib/quietport/hub.db ".backup '$STAGE/hub/hub.db'"
  cp -r /var/lib/quietport/policy "$STAGE/hub/"
  cp /etc/garage/garage.toml "$STAGE/hub/"
  cp /etc/quietport/hub.env "$STAGE/hub/"
  # garage: metadata snapshot (consistent), data dir is content-addressed blobs (safe to copy live)
  garage -c /etc/garage/garage.toml meta snapshot >/dev/null 2>&1 || true
  export RCLONE_CONFIG_CRYPT_TYPE=crypt RCLONE_CONFIG_CRYPT_REMOTE="off:${BACKUP_BUCKET}/hub" RCLONE_CONFIG_CRYPT_PASSWORD="$BACKUP_CRYPT_PASSWORD" RCLONE_CONFIG_CRYPT_PASSWORD2="$BACKUP_CRYPT_SALT"
  rclone sync "$STAGE" crypt:state --transfers 4 -q
  rclone sync /var/lib/garage/meta/snapshots crypt:garage-meta-snapshots --transfers 4 -q --max-age 7d
  rclone sync /var/lib/garage/data crypt:garage-data --transfers 8 --checkers 16 -q
  # canary object for the weekly restore test (FR-75)
  printf 'quietport backup canary %s\n' "$START" > "$STAGE/canary.txt"
  sha256sum "$STAGE/canary.txt" | cut -d' ' -f1 > "$STATE/canary.sha256"
  rclone copyto "$STAGE/canary.txt" crypt:canary/canary.txt -q
} 2>"$STATE/last.err" || { RESULT=failed; DETAIL=$(tail -3 "$STATE/last.err" | tr '\n' ' '); }
printf '{"started":"%s","finished":"%s","result":"%s","detail":%s}\n' "$START" "$(date -Iseconds)" "$RESULT" "$(printf '%s' "$DETAIL" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')" > "$STATE/last.json"
chown quietport:quietport "$STATE"/*.json 2>/dev/null || true
echo "backup $RESULT $DETAIL"
