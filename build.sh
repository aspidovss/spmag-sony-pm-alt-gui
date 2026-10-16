#!/usr/bin/env bash
# Builds the Windows executables, Linux binaries, .deb and AppImage packages into dist/.
# Requires Go 1.22+ (rsrc is installed automatically).
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${VERSION:-0.1.0}"
# -mod=mod lets Go fill in missing go.sum entries on the first build.
export GOFLAGS="${GOFLAGS:--mod=mod}"
command -v rsrc >/dev/null || go install github.com/akavel/rsrc@latest
export PATH="$PATH:$(go env GOPATH)/bin"
go run ./tools/genres
rsrc -manifest build/app.manifest -ico build/app.ico -arch amd64 -o cmd/WifiPhotoReceiver/rsrc_windows_amd64.syso
rsrc -ico build/app.ico -arch amd64 -o cmd/wpr/rsrc_windows_amd64.syso
mkdir -p dist
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=$VERSION" -o dist/SPMAG-WifiPhotoReceiver.exe ./cmd/WifiPhotoReceiver
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o dist/wpr.exe ./cmd/wpr
cp README.md LICENSE dist/
# Linux: command-line tool / service (wpr watch)
for arch in amd64 arm64; do
  mkdir -p "dist/linux-$arch"
  GOOS=linux GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "dist/linux-$arch/wpr" ./cmd/wpr
  cp README.md LICENSE packaging/linux/spmag-wpr.service "dist/linux-$arch/"
done
# Debian packages (skipped if dpkg-deb is missing)
if command -v dpkg-deb >/dev/null; then
  for arch in amd64 arm64; do
    bash packaging/linux/build-deb.sh "$arch" "$VERSION" "dist/linux-$arch/wpr" dist
  done
else
  echo "dpkg-deb not found: skipping .deb packages"
fi
# AppImage (needs curl; appimagetool is downloaded on first use; SKIP_APPIMAGE=1 skips)
if [ -z "${SKIP_APPIMAGE:-}" ] && command -v curl >/dev/null; then
  for arch in amd64 arm64; do
    if ! bash packaging/linux/appimage/build-appimage.sh "$arch" "$VERSION" "dist/linux-$arch/wpr" build/app-256.png dist; then
      [ -n "${CI:-}" ] && exit 1   # on a build server a missing AppImage is an error
      echo "AppImage for $arch could not be built (offline?): skipped"
    fi
  done
else
  echo "AppImage skipped"
fi
ls -la dist dist/linux-*
