#!/bin/sh
# Build MiniCRM and package it as a macOS .app bundle you can double-click.
#
# Usage:
#   ./make-mac-app.sh arm64 1.0.0   # Apple Silicon, version 1.0.0
#   ./make-mac-app.sh amd64 1.0.0   # Intel Mac, version 1.0.0
#   ./make-mac-app.sh arm64 1.1.0   # Apple Silicon, custom version
#
# Requires: Go (https://go.dev) and Xcode Command Line Tools (xcode-select --install)

set -e

ARCH="${1:-arm64}"
VERSION="${2:-1.0.0}"
APP_NAME="MiniCRM"

case "$ARCH" in
  arm64|amd64) ;;
  *)
    echo "Usage: $0 [arm64|amd64] [version]" >&2
    exit 1
    ;;
esac

echo "==> Building minicrm (darwin/$ARCH, version $VERSION)"
GOOS=darwin GOARCH="$ARCH" go build \
  -ldflags "-X main.version=$VERSION" \
  -o minicrm-build .

echo "==> Creating $APP_NAME.app"
rm -rf "$APP_NAME.app"
mkdir -p "$APP_NAME.app/Contents/MacOS" "$APP_NAME.app/Contents/Resources"

# The program is named minicrm-server, not minicrm: macOS file names ignore upper and lower
# case, so "minicrm" and the launcher "MiniCRM" below would be one and the same file.
cp minicrm-build "$APP_NAME.app/Contents/MacOS/minicrm-server"
chmod +x "$APP_NAME.app/Contents/MacOS/minicrm-server"

# A double-clicked app has no Terminal and no working folder, so this launcher says where the
# data lives (~/MiniCRM/data, the folder the README uses) and opens the app in the browser.
# If MiniCRM is already running, the second copy exits and the browser opens the first one.
cat > "$APP_NAME.app/Contents/MacOS/$APP_NAME" <<'LAUNCHER'
#!/bin/sh
export DB_PATH="${DB_PATH:-$HOME/Library/Application Support/minicrm/crm.db}"
export PORT="${PORT:-4300}"
SERVER="$(dirname "$0")/minicrm-server"
# Never run anything but the real program: a launcher that started itself would loop forever.
if [ ! -x "$SERVER" ] || [ "$SERVER" -ef "$0" ]; then
  echo "MiniCRM.app is damaged: minicrm-server is missing. Build it again." >&2
  exit 1
fi
# Stop any running instance: first try graceful SIGTERM, then force-kill by port.
killall minicrm-server 2>/dev/null || true
# Wait up to 3 s for the port to be released, then force-kill if still occupied.
_i=0
while [ "$_i" -lt 6 ]; do
  _pid=$(lsof -ti tcp:"$PORT" 2>/dev/null)
  [ -z "$_pid" ] && break
  kill -9 "$_pid" 2>/dev/null || true
  sleep 0.5
  _i=$((_i + 1))
done
[ -n "$MINICRM_NO_BROWSER" ] || (sleep 1; open "http://127.0.0.1:$PORT") &
exec "$SERVER"
LAUNCHER
chmod +x "$APP_NAME.app/Contents/MacOS/$APP_NAME"

cat > "$APP_NAME.app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key><string>$APP_NAME</string>
	<key>CFBundleIdentifier</key><string>com.jplassnibatt.minicrm</string>
	<key>CFBundleName</key><string>$APP_NAME</string>
	<key>CFBundleDisplayName</key><string>$APP_NAME</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleVersion</key><string>$VERSION</string>
	<key>CFBundleShortVersionString</key><string>$VERSION</string>
	<key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST

echo "==> Ad-hoc signing $APP_NAME.app"
codesign --sign - --force --deep "$APP_NAME.app" 2>/dev/null || true

rm minicrm-build

echo "==> Done: $APP_NAME.app"
echo "    Double-click it. Your browser opens http://127.0.0.1:4300"
echo "    Your data is kept in ~/Library/Application Support/minicrm"
