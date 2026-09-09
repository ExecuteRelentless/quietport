#!/bin/bash
# Build the double-click installers: a notarized universal "Quietport Installer.app" for macOS and a windowsgui exe.
# Usage: scripts/build-installer.sh <version> <hub host>
# Env: NOTARY_KEY (p8 path), NOTARY_KEY_ID, NOTARY_ISSUER for notarization; skipped when absent (app is only signed).
set -euo pipefail
V="${1:?version}"; HOST="${2:?hub host}"
R=$(cd "$(dirname "$0")/.." && pwd); cd "$R"
# Build outside ~/Documents: Finder/iCloud stamps com.apple.FinderInfo on new .app bundles there and codesign refuses it.
OUT="/private/tmp/qp-installer-build/$V"; rm -rf "$OUT"; mkdir -p "$OUT" "$R/dist/$V"
IDENTITY="${SIGN_IDENTITY:-Developer ID Application}" # the login keychain holds one Developer ID Application identity
PUB=$(cat "$R/../release-keys/release.pub" 2>/dev/null || echo "")
LD="-s -w -X main.Version=$V -X main.DefaultHost=$HOST -X quietport.app/quietport/internal/agent.Version=$V -X quietport.app/quietport/internal/agent.ReleasePubKey=$PUB"

echo "== macOS universal binary"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$LD" -o "$OUT/qpi-arm64" ./cmd/qp-installer
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$LD" -o "$OUT/qpi-amd64" ./cmd/qp-installer
lipo -create -output "$OUT/quietport-installer" "$OUT/qpi-arm64" "$OUT/qpi-amd64"
rm "$OUT/qpi-arm64" "$OUT/qpi-amd64"

APP="$OUT/Quietport Installer.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources/client"
mv "$OUT/quietport-installer" "$APP/Contents/MacOS/quietport-installer"

echo "== bundle the whole client inside the app (universal binaries)"
CA="$R/dist/$V/client-darwin-arm64"; CX="$R/dist/$V/client-darwin-amd64"
[ -d "$CA" ] && [ -d "$CX" ] || { echo "run scripts/build.sh $V first (client bundles missing)"; exit 1; }
for f in qpsync-agent qpctl rclone tailscaled tailscale; do
  lipo -create -output "$APP/Contents/Resources/client/$f" "$CA/$f" "$CX/$f"
done
cp "$R/installers/mac/qp-sidebar" "$APP/Contents/Resources/client/qp-sidebar"
echo "$V" > "$APP/Contents/Resources/client/VERSION"
chmod 755 "$APP/Contents/Resources/client"/*
cp "$R/installers/mac/AppIcon.icns" "$APP/Contents/Resources/AppIcon.icns" 2>/dev/null || true
cat > "$APP/Contents/Info.plist" <<X
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleDevelopmentRegion</key><string>en</string>
<key>CFBundleExecutable</key><string>quietport-installer</string>
<key>CFBundleIdentifier</key><string>app.quietport.installer</string>
<key>CFBundleName</key><string>Quietport Installer</string>
<key>CFBundleDisplayName</key><string>Quietport Installer</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>$V</string>
<key>CFBundleVersion</key><string>$V</string>
<key>CFBundleIconFile</key><string>AppIcon</string>
<key>LSMinimumSystemVersion</key><string>12.0</string>
<key>NSHighResolutionCapable</key><true/>
<key>NSHumanReadableCopyright</key><string>Quietport</string>
</dict></plist>
X
echo "== sign (hardened runtime, timestamp)"
xattr -cr "$APP"   # Documents/iCloud adds FinderInfo xattrs that codesign refuses
# notarization requires every Mach-O inside the bundle to carry a hardened-runtime signature
for f in qpsync-agent qpctl rclone tailscaled tailscale qp-sidebar; do
  codesign --force --options runtime --timestamp --sign "$IDENTITY" "$APP/Contents/Resources/client/$f"
done
codesign --force --options runtime --timestamp --sign "$IDENTITY" "$APP"
codesign --verify --deep --strict --verbose=2 "$APP"

if [ -n "${NOTARY_PROFILE:-}" ]; then
  echo "== notarize (keychain profile $NOTARY_PROFILE)"
  ditto -c -k --keepParent "$APP" "$OUT/notarize.zip"
  xcrun notarytool submit "$OUT/notarize.zip" --keychain-profile "$NOTARY_PROFILE" --wait
  xcrun stapler staple "$APP"
  rm "$OUT/notarize.zip"
  spctl -a -vv -t exec "$APP/Contents/MacOS/quietport-installer" || true
elif [ -n "${NOTARY_KEY:-}" ] && [ -n "${NOTARY_KEY_ID:-}" ] && [ -n "${NOTARY_ISSUER:-}" ]; then
  echo "== notarize"
  ditto -c -k --keepParent "$APP" "$OUT/notarize.zip"
  xcrun notarytool submit "$OUT/notarize.zip" --key "$NOTARY_KEY" --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER" --wait
  xcrun stapler staple "$APP"
  rm "$OUT/notarize.zip"
  spctl -a -vv -t exec "$APP/Contents/MacOS/quietport-installer" || true
else
  echo "== NOT notarized (set NOTARY_KEY, NOTARY_KEY_ID, NOTARY_ISSUER); Gatekeeper will block this on download"
fi
(cd "$OUT" && COPYFILE_DISABLE=1 tar czf "$R/dist/$V/quietport-installer-darwin.tar.gz" "Quietport Installer.app")

echo "== disk image (one file to download)"
DMG="$OUT/Quietport Installer.dmg"
rm -f "$DMG"; mkdir -p "$OUT/dmgroot"; rm -rf "$OUT/dmgroot"/*; cp -R "$APP" "$OUT/dmgroot/"
hdiutil create -volname "Quietport Installer" -srcfolder "$OUT/dmgroot" -ov -format UDZO -quiet "$DMG"
codesign --force --timestamp --sign "$IDENTITY" "$DMG"
if [ -n "${NOTARY_PROFILE:-}" ]; then
  xcrun notarytool submit "$DMG" --keychain-profile "$NOTARY_PROFILE" --wait | tail -3
  xcrun stapler staple "$DMG"
fi
spctl -a -vv -t open --context context:primary-signature "$DMG" 2>&1 | tail -2 || true
cp "$DMG" "$R/dist/$V/quietport-installer-darwin.dmg"

echo "== Windows installer exe (client zip embedded)"
WZ="$R/dist/$V/quietport-windows-amd64-$V.zip"
[ -f "$WZ" ] || { echo "run scripts/build.sh $V first ($WZ missing)"; exit 1; }
cp "$WZ" "$R/cmd/qp-installer/bundle.zip"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$LD -H windowsgui" -o "$R/dist/$V/quietport-installer-windows-amd64.exe" ./cmd/qp-installer
rm -f "$R/cmd/qp-installer/bundle.zip"
ls -la "$R/dist/$V/quietport-installer-darwin.tar.gz" "$R/dist/$V/quietport-installer-darwin.dmg" "$R/dist/$V/quietport-installer-windows-amd64.exe"
