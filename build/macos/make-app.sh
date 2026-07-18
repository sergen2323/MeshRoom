#!/bin/bash
# Сборка MeshRoom.app + .dmg для macOS.
# Использование: build/macos/make-app.sh [версия] [arm64|amd64|universal]
set -euo pipefail

VERSION="${1:-0.2.0}"
ARCH="${2:-universal}"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/dist"
APP="$OUT/MeshRoom.app"

mkdir -p "$OUT"
rm -rf "$APP"

echo "==> building binaries ($ARCH)"
cd "$ROOT"
build_one() { # goarch outfile — cgo обязателен: нативное окно (WKWebView)
  local carch="arm64"
  [ "$1" = "amd64" ] && carch="x86_64"
  CGO_ENABLED=1 GOOS=darwin GOARCH="$1" \
    CGO_CFLAGS="-arch $carch" CGO_CXXFLAGS="-arch $carch" CGO_LDFLAGS="-arch $carch" \
    go build -mod=mod -buildvcs=false \
    -ldflags "-s -w" -o "$2" ./cmd/meshroom
}
case "$ARCH" in
  universal)
    build_one arm64 "$OUT/meshroom-arm64"
    build_one amd64 "$OUT/meshroom-amd64"
    lipo -create -output "$OUT/meshroom-bin" "$OUT/meshroom-arm64" "$OUT/meshroom-amd64"
    rm -f "$OUT/meshroom-arm64" "$OUT/meshroom-amd64"
    ;;
  *)
    build_one "$ARCH" "$OUT/meshroom-bin"
    ;;
esac

echo "==> app bundle"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$OUT/meshroom-bin" "$APP/Contents/MacOS/MeshRoom"
rm -f "$OUT/meshroom-bin"

# .icns из build/icon.png (свой генератор — не зависит от sips/iconutil)
go run ./build/gen-icns -in "$ROOT/build/icon.png" -o "$APP/Contents/Resources/MeshRoom.icns"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleName</key><string>MeshRoom</string>
  <key>CFBundleDisplayName</key><string>MeshRoom</string>
  <key>CFBundleIdentifier</key><string>app.meshroom.desktop</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleExecutable</key><string>MeshRoom</string>
  <key>CFBundleIconFile</key><string>MeshRoom</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHumanReadableCopyright</key><string>MeshRoom — virtual LAN without servers</string>
</dict></plist>
PLIST

# ad-hoc подпись, чтобы Gatekeeper не ругался на «повреждённое» приложение
codesign --force --deep -s - "$APP" 2>/dev/null || true

echo "==> dmg"
DMG="$OUT/MeshRoom-${VERSION}-macos.dmg"
rm -f "$DMG"
STAGE="$OUT/dmg-stage"
rm -rf "$STAGE"; mkdir -p "$STAGE"
cp -R "$APP" "$STAGE/"
ln -s /Applications "$STAGE/Applications"
hdiutil create -volname "MeshRoom" -srcfolder "$STAGE" -ov -format UDZO "$DMG" >/dev/null
rm -rf "$STAGE"

echo "done: $DMG"
