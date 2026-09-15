#!/bin/bash
# Build the double-click installers: a notarized universal "Quietport Installer.app" for macOS and a windowsgui exe.
# The Mac app carries no client: it downloads quietport-darwin-<its chip>-<version>.tar.gz and installs it only if its
# sha256 is the one compiled in below (docs/adr/0027), so publish the tarballs with the installer, never after it.
# Usage: scripts/build-installer.sh <version> <hub host>
# Env: NOTARY_KEY (p8 path), NOTARY_KEY_ID, NOTARY_ISSUER for notarization; skipped when absent (app is only signed).
#      WINDOWS_INSTALLER=<signed Quietport.exe from the tag's CI run> uses CI's signed installer (docs/SIGNING.md).
set -euo pipefail
V="${1:?version}"; HOST="${2:?hub host}"
R=$(cd "$(dirname "$0")/.." && pwd); cd "$R"
# Build outside ~/Documents: Finder/iCloud stamps com.apple.FinderInfo on new .app bundles there and codesign refuses it.
OUT="/private/tmp/qp-installer-build/$V"; rm -rf "$OUT"; mkdir -p "$OUT" "$R/dist/$V"
IDENTITY="${SIGN_IDENTITY:-Developer ID Application}" # the login keychain holds one Developer ID Application identity
PUB=$(cat "$R/../release-keys/release.pub" 2>/dev/null || echo "")
LDW="-X main.Version=$V -X quietport.app/quietport/internal/agent.Version=$V -X quietport.app/quietport/internal/agent.ReleasePubKey=$PUB"
LD="-s -w -X main.Version=$V -X main.DefaultHost=$HOST -X quietport.app/quietport/internal/agent.Version=$V -X quietport.app/quietport/internal/agent.ReleasePubKey=$PUB"

CA="$R/dist/$V/quietport-darwin-arm64-$V.tar.gz"; CX="$R/dist/$V/quietport-darwin-amd64-$V.tar.gz"
[ -f "$CA" ] && [ -f "$CX" ] || { echo "run scripts/build.sh $V first (client bundles missing)"; exit 1; }
PIN="-X main.clientSumArm64=$(shasum -a 256 "$CA" | cut -d' ' -f1) -X main.clientSumAmd64=$(shasum -a 256 "$CX" | cut -d' ' -f1)"

echo "== macOS universal installer (the client for each chip is downloaded, pinned by $PIN)"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$LD $PIN" -o "$OUT/qpi-arm64" ./cmd/qp-installer
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$LD $PIN" -o "$OUT/qpi-amd64" ./cmd/qp-installer
lipo -create -output "$OUT/quietport-installer" "$OUT/qpi-arm64" "$OUT/qpi-amd64"
rm "$OUT/qpi-arm64" "$OUT/qpi-amd64"

APP="$OUT/Quietport Installer.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
mv "$OUT/quietport-installer" "$APP/Contents/MacOS/quietport-installer"
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
elif [ -n "${NOTARY_KEY:-}" ] && [ -n "${NOTARY_KEY_ID:-}" ] && [ -n "${NOTARY_ISSUER:-}" ]; then
  xcrun notarytool submit "$DMG" --key "$NOTARY_KEY" --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER" --wait | tail -3
  xcrun stapler staple "$DMG"
fi
spctl -a -vv -t open --context context:primary-signature "$DMG" 2>&1 | tail -2 || true
cp "$DMG" "$R/dist/$V/quietport-installer-darwin.dmg"

echo "== Linux installers (client tarball embedded, terminal prompts)"
for a in amd64 arm64; do
  LT="$R/dist/$V/quietport-linux-$a-$V.tar.gz"
  [ -f "$LT" ] || { echo "run scripts/build.sh $V first ($LT missing)"; exit 1; }
  cp "$LT" "$R/cmd/qp-installer/bundle/bundle.tar.gz"
  CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "$LD" -o "$R/dist/$V/quietport-installer-linux-$a" ./cmd/qp-installer
  rm -f "$R/cmd/qp-installer/bundle/bundle.tar.gz"
done

echo "== Windows installer exe (client zip embedded)"
WZ="$R/dist/$V/quietport-windows-amd64-$V.zip"
[ -f "$WZ" ] || { echo "run scripts/build.sh $V first ($WZ missing)"; exit 1; }
if [ -n "${WINDOWS_INSTALLER:-}" ]; then
  # the signed installer from the tag's `windows` workflow (artifact windows-installer-signed, docs/SIGNING.md). It
  # must be the installer CI built around the same client zip that build.sh took from CI, so that a member who
  # installs gets the programs a self-update would give them
  [ -n "${WINDOWS_CLIENT_ZIP:-}" ] || { echo "WINDOWS_INSTALLER needs the WINDOWS_CLIENT_ZIP from the same CI run"; exit 1; }
  cp "$WINDOWS_INSTALLER" "$R/dist/$V/quietport-installer-windows-amd64.exe"
else
  cp "$WZ" "$R/cmd/qp-installer/bundle/bundle.zip"
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$LDW -H windowsgui" -o "$R/dist/$V/quietport-installer-windows-amd64.exe" ./cmd/qp-installer
  rm -f "$R/cmd/qp-installer/bundle/bundle.zip"
fi
echo "== add the installers to the signed checksum list"
if [ -f "$R/../release-keys/release.key" ]; then
  for f in "$R/dist/$V/quietport-installer-darwin.dmg" "$R/dist/$V/quietport-installer-windows-amd64.exe" "$R/dist/$V/quietport-installer-darwin.tar.gz" "$R/dist/$V/quietport-installer-linux-amd64" "$R/dist/$V/quietport-installer-linux-arm64"; do
    sha=$(shasum -a 256 "$f" | cut -d' ' -f1); sig=$(cd "$R" && go run ./scripts/sign -key "$R/../release-keys/release.key" -msg "$sha")
    grep -v " $(basename "$f") " "$R/dist/$V/SHA256SUMS.signed" > "$R/dist/$V/SHA256SUMS.tmp" 2>/dev/null || true
    echo "$sha  $(basename "$f")  $sig" >> "$R/dist/$V/SHA256SUMS.tmp"; mv "$R/dist/$V/SHA256SUMS.tmp" "$R/dist/$V/SHA256SUMS.signed"
  done
fi
ls -la "$R/dist/$V/quietport-installer-darwin.tar.gz" "$R/dist/$V/quietport-installer-darwin.dmg" "$R/dist/$V/quietport-installer-windows-amd64.exe"
