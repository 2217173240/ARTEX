#!/bin/bash
# Package an already built native-channel binary. No install hooks or user state.
set -euo pipefail
usage() {
  echo "Usage: $0 --binary PATH --version X.Y.Z --arch amd64|arm64 --outdir PATH" >&2
  exit 2
}
binary= version= arch= outdir=
while [ "$#" -gt 0 ]; do
  [ "$#" -ge 2 ] || usage
  case "$1" in
    --binary) binary=$2 ;;
    --version) version=$2 ;;
    --arch) arch=$2 ;;
    --outdir) outdir=$2 ;;
    *) usage ;;
  esac
  shift 2
done
[ -n "$binary" ] && [ -n "$version" ] && [ -n "$arch" ] && [ -n "$outdir" ] || usage
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Version must be X.Y.Z' >&2; exit 2; }
case "$arch" in amd64) macho_arch=x86_64 ;; arm64) macho_arch=arm64 ;; *) usage ;; esac
[ "$(uname -s)" = Darwin ] || { echo 'Packaging requires macOS.' >&2; exit 1; }
for tool in pkgbuild productbuild lipo sips iconutil plutil git; do
  command -v "$tool" >/dev/null || { echo "Missing tool: $tool" >&2; exit 1; }
done
[ -f "$binary" ] || { echo "Binary not found: $binary" >&2; exit 1; }
# Reject Linux executables and accidentally mislabeled/universal payloads.
[ "$(lipo -archs "$binary")" = "$macho_arch" ] || { echo "Binary must contain only $macho_arch" >&2; exit 1; }
repo=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$outdir"
outdir=$(cd "$outdir" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/artex-pkg.XXXXXXXX")
trap 'rm -rf "$work"' EXIT
app="$work/root/ARTEX.app"
resources="$app/Contents/Resources"
mkdir -p "$app/Contents/MacOS" "$resources" "$work/ARTEX.iconset"
cp "$binary" "$app/Contents/MacOS/artex"
chmod 755 "$app/Contents/MacOS/artex"
sed "s/@VERSION@/$version/g" "$repo/packaging/macos/Info.plist.in" > "$app/Contents/Info.plist"
plutil -lint "$app/Contents/Info.plist"
# Only allowlisted release resources: never copy local runtime config/data.
cp "$repo/LICENSE" "$repo/CHANGELOG.md" "$repo/config.example.json" "$resources/"
# Git-tracked skill files exclude local/generated files and credentials.
while IFS= read -r -d '' path; do
  [ ! -L "$repo/$path" ] || { echo "Skill symlinks are not supported: $path" >&2; exit 1; }
  mkdir -p "$resources/$(dirname "$path")"
  cp "$repo/$path" "$resources/$path"
done < <(git -C "$repo" ls-files -z -- skills)
for size in 16 32 128 256 512; do
  sips -z "$size" "$size" "$repo/web/public/logo.png" --out "$work/ARTEX.iconset/icon_${size}x${size}.png" >/dev/null
  double=$((size * 2))
  sips -z "$double" "$double" "$repo/web/public/logo.png" --out "$work/ARTEX.iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$work/ARTEX.iconset" -o "$resources/ARTEX.icns"
# Payload belongs to /Applications; installation never launches ARTEX as root.
pkgbuild --root "$work/root" --install-location /Applications \
  --component-plist "$repo/packaging/macos/components.plist" \
  --identifier io.github.2217173240.artex --version "$version" \
  --ownership recommended "$work/component.pkg"
cat > "$work/Distribution.xml" <<XML
<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
  <title>ARTEX $version</title>
  <options customize="never" require-scripts="false" hostArchitectures="$macho_arch"/>
  <domains enable_localSystem="true" enable_currentUserHome="false" enable_anywhere="false"/>
  <allowed-os-versions><os-version min="12.0"/></allowed-os-versions>
  <choices-outline><line choice="artex"/></choices-outline>
  <choice id="artex" visible="false" title="ARTEX"><pkg-ref id="io.github.2217173240.artex"/></choice>
  <pkg-ref id="io.github.2217173240.artex" version="$version" onConclusion="none">component.pkg</pkg-ref>
</installer-gui-script>
XML
package="$outdir/artex-$version-darwin-$arch.pkg"
if [ -n "${PKG_SIGNATURE_ID:-}" ]; then
  productbuild --distribution "$work/Distribution.xml" --package-path "$work" \
    --sign "$PKG_SIGNATURE_ID" "$package"
else
  productbuild --distribution "$work/Distribution.xml" --package-path "$work" "$package"
fi
echo "$package"
