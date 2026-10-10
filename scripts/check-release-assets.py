#!/usr/bin/env python3
"""Fail closed unless the complete native and portable asset set is present."""
import argparse
import hashlib
import json
from pathlib import Path
import re


def expected(version):
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', version):
        raise ValueError('Version must be X.Y.Z')
    targets = ['linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64', 'windows-amd64', 'windows-arm64']
    assets = {f'artex-{version}-{target}.zip' for target in targets}
    for platform, extensions in [('linux', ['tar.gz', 'deb', 'rpm']), ('darwin', ['pkg']), ('windows', ['msi'])]:
        assets.update(f'artex-{version}-{platform}-{arch}.{ext}' for arch in ['amd64', 'arm64'] for ext in extensions)
    return assets


def validate_local(directory, version, write=False):
    names = expected(version)
    actual = {p.name for p in directory.iterdir() if p.is_file()} - {'SHA256SUMS', 'IMAGE_DIGEST'}
    if actual != names:
        raise ValueError(f'Asset mismatch: missing={sorted(names-actual)}, unexpected={sorted(actual-names)}')
    lines = []
    for name in sorted(names):
        path = directory / name
        if path.stat().st_size == 0:
            raise ValueError(f'Empty asset: {name}')
        lines.append(f'{hashlib.sha256(path.read_bytes()).hexdigest()}  {name}\n')
    checksums = ''.join(lines)
    if write:
        (directory / 'SHA256SUMS').write_text(checksums)
    elif (directory / 'SHA256SUMS').read_text() != checksums:
        raise ValueError('Checksum coverage or content mismatch')


def validate_remote(document, version):
    if document.get('isDraft') is not True:
        raise ValueError('Release must still be a draft')
    assets = document['assets']
    names = [a['name'] for a in assets]
    required = expected(version) | {'SHA256SUMS'}
    if len(names) != len(set(names)) or not required <= set(names) or set(names) - required - {'IMAGE_DIGEST'}:
        raise ValueError('Published draft has incomplete or unexpected assets')
    if any(a['size'] <= 0 for a in assets):
        raise ValueError('Published draft has empty assets')

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', required=True)
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument('--directory', type=Path)
    group.add_argument('--release-json', type=Path)
    parser.add_argument('--write-checksums', action='store_true')
    args = parser.parse_args()
    if args.directory:
        validate_local(args.directory, args.version, args.write_checksums)
    else:
        validate_remote(json.loads(args.release_json.read_text()), args.version)
