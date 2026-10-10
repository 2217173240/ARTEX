#!/usr/bin/env python3
"""Convert an allowlisted Linux portable ZIP to a tarball without extraction."""
import argparse
import pathlib
import tarfile
import zipfile
import io


def convert(source, target):
    with zipfile.ZipFile(source) as archive, tarfile.open(target, 'w:gz') as output:
        for item in archive.infolist():
            path = pathlib.PurePosixPath(item.filename)
            if path.is_absolute() or '..' in path.parts or '\\' in item.filename:
                raise ValueError('Unsafe ZIP path')
            mode = item.external_attr >> 16
            if mode & 0o170000 == 0o120000:
                raise ValueError('ZIP symlinks are unsupported')
            info = tarfile.TarInfo(item.filename)
            info.mtime = 0
            info.mode = 0o755 if item.is_dir() or path.name == 'artex' else 0o644
            if item.is_dir():
                info.type = tarfile.DIRTYPE
                output.addfile(info)
            else:
                content = archive.read(item)
                info.size = len(content)
                output.addfile(info, io.BytesIO(content))

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('source', type=pathlib.Path)
    parser.add_argument('target', type=pathlib.Path)
    args = parser.parse_args()
    convert(args.source, args.target)
