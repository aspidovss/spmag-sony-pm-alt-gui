#!/usr/bin/env bash
# Builds an AppImage of the wpr tool:
#   build-appimage.sh ARCH VERSION WPR_BINARY ICON_PNG OUTDIR
# ARCH is amd64 or arm64 (the binary must match). The result is
# OUTDIR/SPMAG-wpr-VERSION-linux-ARCH.AppImage.
#
# appimagetool and the AppImage runtime are downloaded once into
# build/appimage-tools (set APPIMAGE_TOOLS to use another folder). appimagetool
# itself is run with --appimage-extract-and-run, so FUSE is not needed to build.
set -euo pipefail
[ $# -eq 5 ] || { echo "usage: $0 ARCH VERSION WPR_BINARY ICON_PNG OUTDIR" >&2; exit 2; }
arch="$1"; version="$2"; bin="$3"; icon="$4"; out="$5"
case "$arch" in
  amd64) ai=x86_64 ;;
  arm64) ai=aarch64 ;;
  *) echo "unknown architecture: $arch" >&2; exit 2 ;;
esac
here="$(cd "$(dirname "$0")" && pwd)"
tools="${APPIMAGE_TOOLS:-$here/../../../build/appimage-tools}"
mkdir -p "$tools" "$out"
tools="$(cd "$tools" && pwd)"; out="$(cd "$out" && pwd)"

fetch() { # NAME URL
  if [ ! -x "$tools/$1" ]; then
    echo "downloading $1"
    curl -fsSL --retry 3 -o "$tools/$1.part" "$2"
    chmod +x "$tools/$1.part"
    mv "$tools/$1.part" "$tools/$1"
  fi
}
fetch appimagetool "https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-x86_64.AppImage"
fetch "runtime-$ai" "https://github.com/AppImage/type2-runtime/releases/download/continuous/runtime-$ai"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
app="$work/SPMAG-wpr.AppDir"
install -Dm755 "$bin" "$app/usr/bin/wpr"
install -Dm755 "$here/AppRun" "$app/AppRun"
install -Dm644 "$here/spmag-wpr.desktop" "$app/spmag-wpr.desktop"
install -Dm644 "$here/spmag-wpr.desktop" "$app/usr/share/applications/spmag-wpr.desktop"
install -Dm644 "$icon" "$app/spmag-wpr.png"
install -Dm644 "$icon" "$app/usr/share/icons/hicolor/256x256/apps/spmag-wpr.png"
ln -s spmag-wpr.png "$app/.DirIcon"

target="$out/SPMAG-wpr-$version-linux-$arch.AppImage"
ARCH="$ai" "$tools/appimagetool" --appimage-extract-and-run --no-appstream \
  --runtime-file "$tools/runtime-$ai" "$app" "$target"
chmod +x "$target"
echo "built $target"
