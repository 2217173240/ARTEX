#!/usr/bin/env python3
"""Stage only Git-tracked, regular skill resources for release packaging."""
import argparse
from pathlib import Path
import shutil
import subprocess


def stage(repo, destination):
    entries = subprocess.check_output(['git', '-C', str(repo), 'ls-files', '-s', '-z', '--', 'skills']).split(b'\0')
    count = 0
    for entry in filter(None, entries):
        metadata, raw_path = entry.split(b'\t', 1)
        mode = metadata.split()[0]
        relative = Path(raw_path.decode())
        source = repo / relative
        if mode not in {b'100644', b'100755'} or source.is_symlink() or not source.is_file():
            raise ValueError(f'Unsupported skill resource: {relative}')
        if not source.resolve().is_relative_to(repo.resolve()):
            raise ValueError(f'Skill resource escapes repository: {relative}')
        target = destination / relative.relative_to('skills')
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)
        target.chmod(0o755 if mode == b'100755' else 0o644)
        count += 1
    if count == 0:
        raise ValueError('No tracked skills were staged')
    return count


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('destination', type=Path)
    args = parser.parse_args()
    print(f'Staged {stage(Path(__file__).resolve().parents[1], args.destination)} tracked skill files')
