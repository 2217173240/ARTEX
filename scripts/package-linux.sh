#!/usr/bin/env bash
# Native packages own only static /usr files. User configuration/data stay in XDG paths.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
binary= version= arch= format= outdir=
while (($#)); do
  case "$1" in
    --binary|--version|--arch|--format|--outdir)
      (($# >= 2)) || { echo "Missing value for $1" >&2; exit 2; }
      case "$1" in
        --binary) binary=$2;; --version) version=$2;; --arch) arch=$2;;
        --format) format=$2;; --outdir) outdir=$2;;
      esac
      shift 2;;
    *) echo "Unknown argument: $1" >&2; exit 2;;
  esac
done
[[ -f "$binary" && -n "$version" && -n "$outdir" ]] || { echo 'Required: --binary --version --arch --format --outdir' >&2; exit 2; }
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo 'Version must be a semantic version without v prefix' >&2; exit 2; }
case "$arch" in amd64|arm64) ;; *) echo 'Architecture must be amd64 or arm64' >&2; exit 2;; esac
case "$format" in deb|rpm) ;; *) echo 'Format must be deb or rpm' >&2; exit 2;; esac
# Read ELF header without executing a cross-architecture or untrusted target binary.
python3 - "$binary" "$arch" <<'PY'
import struct, sys
with open(sys.argv[1], 'rb') as f:
    header = f.read(20)
expected = {'amd64': 62, 'arm64': 183}[sys.argv[2]]
if len(header) != 20 or header[:4] != b'\x7fELF' or header[4:6] != b'\x02\x01' or struct.unpack('<H', header[18:20])[0] != expected:
    sys.exit('Binary must be a little-endian 64-bit Linux ELF matching --arch')
PY
binary=$(cd "$(dirname "$binary")" && pwd)/$(basename "$binary")
mkdir -p "$outdir"
outdir=$(cd "$outdir" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
nfpm=${NFPM_BIN:-}
if [[ -z "$nfpm" ]]; then
  os=$(uname -s)
  case "$(uname -m)" in arm64|aarch64) host_arch=arm64;; x86_64|amd64) host_arch=x86_64;; *) echo 'Unsupported packaging host architecture' >&2; exit 2;; esac
  case "$os/$host_arch" in
    Darwin/arm64) checksum=e8c9d1d9ac218eeed479375143dc46b8d51a2b8dbba8e2f9f15ecc8faa2e404b;;
    Darwin/x86_64) checksum=2b04108f8757313dde92ed729560845aadfb7782887eb6988a5dd96f9c146861;;
    Linux/arm64) checksum=1c0f5f2999b9a974bfb04fdb0cc3306096de530ac5dbb25d739cc5f5219c919c;;
    Linux/x86_64) checksum=0660ca602b2d2d2ae4781a06c692b3eeb9d437ffea05b831d76e41f4a3188783;;
    *) echo 'Packaging requires Linux or macOS' >&2; exit 2;;
  esac
  archive="nfpm_2.47.0_${os}_${host_arch}.tar.gz"
  curl --fail --location --retry 3 --silent --show-error "https://github.com/goreleaser/nfpm/releases/download/v2.47.0/$archive" -o "$tmp/$archive"
  python3 - "$tmp/$archive" "$checksum" <<'PY'
import hashlib, sys
with open(sys.argv[1], 'rb') as f:
    digest = hashlib.sha256()
    for chunk in iter(lambda: f.read(1024 * 1024), b''):
        digest.update(chunk)
    actual = digest.hexdigest()
if actual != sys.argv[2]:
    sys.exit('nFPM download checksum mismatch')
PY
  tar -xzf "$tmp/$archive" -C "$tmp" nfpm
  nfpm="$tmp/nfpm"
fi
"$nfpm" --version | grep -Eq '(^|[[:space:]])2\.47\.0([[:space:]]|,|$)' || { echo 'nFPM v2.47.0 is required' >&2; exit 2; }
export ARTEX_PACKAGE_ROOT="$root" ARTEX_PACKAGE_BINARY="$binary"
python3 "$root/scripts/stage-skills.py" "$tmp/skills"
export ARTEX_PACKAGE_SKILLS="$tmp/skills"
export ARTEX_PACKAGE_VERSION="$version" ARTEX_PACKAGE_ARCH="$arch"
python3 - "$root/packaging/linux/nfpm.yaml" "$tmp/nfpm.yaml" <<'PYCONFIG'
import os, sys, json
with open(sys.argv[1]) as f:
    config = f.read()
for key in ('ARTEX_PACKAGE_ROOT', 'ARTEX_PACKAGE_BINARY', 'ARTEX_PACKAGE_VERSION', 'ARTEX_PACKAGE_ARCH', 'ARTEX_PACKAGE_SKILLS'):
    value = os.environ[key]
    if '\n' in value or '\r' in value:
        sys.exit('Package paths must not contain newlines')
    config = config.replace('${' + key + '}', json.dumps(value)[1:-1])
with open(sys.argv[2], 'w') as f:
    f.write(config)
PYCONFIG
"$nfpm" package --config "$tmp/nfpm.yaml" --packager "$format" --target "$outdir/artex-$version-linux-$arch.$format"
