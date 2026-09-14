#!/bin/sh
# Quietport installer for macOS. Personalised for one invitation. Runs entirely as the current user.
set -e
HOST="__HOST__"; CODE="__CODE__"; SUPPORT="__SUPPORT__"; OPERATOR="__OPERATOR__"
APP="$HOME/Library/Application Support/Quietport"
fail() { echo "Quietport could not be installed: $1 Please contact $OPERATOR ($SUPPORT)."; exit 1; }
case "$(uname -m)" in arm64) ARCH=arm64;; x86_64) ARCH=amd64;; *) fail "this Mac's processor is not supported.";; esac
# a computer that already has Quietport is never installed again: the link adds a folder to what is here (ADR 0019)
if grep -q '"device_id": [1-9]' "$APP/config.json" 2>/dev/null; then exec "$APP/qpsync-agent" join "$CODE"; fi
mkdir -p "$APP" || fail "the application folder could not be created."
curl -fsSL "https://$HOST/dl/quietport-darwin-$ARCH.tar.gz" -o "$APP/bundle.tar.gz" || fail "the download did not complete."
tar -xzf "$APP/bundle.tar.gz" -C "$APP" || fail "the download was damaged."
rm -f "$APP/bundle.tar.gz"
chmod 700 "$APP"
curl -fsSL "https://$HOST/j/$CODE/payload" -o "$APP/payload.json" || fail "this invitation link is no longer valid."
exec "$APP/qpsync-agent" install --code "$CODE" --payload "$APP/payload.json"
