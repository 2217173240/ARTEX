#!/usr/bin/env bash
set -euo pipefail

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) archive=nfpm_2.47.0_Linux_x86_64.tar.gz; checksum=0660ca602b2d2d2ae4781a06c692b3eeb9d437ffea05b831d76e41f4a3188783;;
  Linux/aarch64|Linux/arm64) archive=nfpm_2.47.0_Linux_arm64.tar.gz; checksum=1c0f5f2999b9a974bfb04fdb0cc3306096de530ac5dbb25d739cc5f5219c919c;;
  Darwin/x86_64) archive=nfpm_2.47.0_Darwin_x86_64.tar.gz; checksum=2b04108f8757313dde92ed729560845aadfb7782887eb6988a5dd96f9c146861;;
  Darwin/arm64) archive=nfpm_2.47.0_Darwin_arm64.tar.gz; checksum=e8c9d1d9ac218eeed479375143dc46b8d51a2b8dbba8e2f9f15ecc8faa2e404b;;
  *) echo 'nFPM setup requires Linux or macOS on x86_64 or arm64' >&2; exit 2;;
esac

root=${RUNNER_TEMP:?RUNNER_TEMP is required}
archive_path="$root/$archive"
binary_path="$root/nfpm"
curl --fail --location --retry 3 --silent --show-error \
  "https://github.com/goreleaser/nfpm/releases/download/v2.47.0/$archive" -o "$archive_path"
python3 - "$archive_path" "$checksum" <<'PY'
import hashlib
import sys

digest = hashlib.sha256()
with open(sys.argv[1], "rb") as source:
    for chunk in iter(lambda: source.read(1024 * 1024), b""):
        digest.update(chunk)
if digest.hexdigest() != sys.argv[2]:
    raise SystemExit("nFPM download checksum mismatch")
PY
tar -xzf "$archive_path" -C "$root" nfpm
test -x "$binary_path"
"$binary_path" --version | grep -Eq '(^|[[:space:]])2\.47\.0([[:space:]]|,|$)'
printf '%s\n' "$binary_path"
