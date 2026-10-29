#!/usr/bin/env bash
# Native macOS build, system SQLite (no Homebrew runtime dependency).
set -euo pipefail
cd "$(dirname "$0")"
if [[ "$(uname -s)" != 'Darwin' ]]; then
  echo 'ERROR: run only on macOS.' >&2; exit 2
fi
command -v go >/dev/null || { echo 'Install a supported Go version.' >&2; exit 2; }
command -v xcrun >/dev/null || { echo 'Run xcode-select --install first.' >&2; exit 2; }
export SDKROOT="$(xcrun --sdk macosx --show-sdk-path)"
export CC="$(xcrun --find clang)"
export CGO_ENABLED=1
export CGO_CFLAGS="-I$SDKROOT/usr/include"
export CGO_LDFLAGS="-L$SDKROOT/usr/lib"
[[ -f "$SDKROOT/usr/include/sqlite3.h" ]] || { echo 'SDK sqlite3.h missing' >&2; exit 2; }
arch="$(go env GOARCH)"
[[ "$arch" == arm64 || "$arch" == amd64 ]] || { echo "Unsupported $arch" >&2; exit 2; }
go test ./internal/sqlite ./internal/store
mkdir -p dist
out="dist/seven-air-link-darwin-$arch"
go build -buildvcs=false -trimpath -ldflags='-s -w' -o "$out" ./cmd/seven-air-link
"$out" version
otool -L "$out" | tee dist/macos-deps.txt
if grep -E '/opt/homebrew/|/usr/local/opt/' dist/macos-deps.txt; then
  echo 'ERROR: unexpected Homebrew runtime dependency' >&2; exit 2
fi
cp "$out" ./seven-air-link
chmod +x ./seven-air-link
echo 'Built ./seven-air-link. Run ./seven-air-link serve --data ./.slb-server --port 7777'
