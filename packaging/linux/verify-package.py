#!/usr/bin/env python3
"""Inspect native package metadata/payload without installing or executing it."""
import io
from pathlib import Path
import struct
import subprocess
import sys
import tarfile

path, arch = Path(sys.argv[1]), sys.argv[2]
required = {
    '/usr/lib/artex/artex', '/usr/bin/artex',
    '/usr/share/applications/artex.desktop',
    '/usr/share/icons/hicolor/scalable/apps/artex.svg',
    '/usr/share/doc/artex/LICENSE', '/usr/share/doc/artex/CHANGELOG.md',
    '/usr/share/doc/artex/config.example.json',
    '/usr/share/artex/skills/api-recon/SKILL.md',
}
if path.suffix == '.deb':
    def archive(name):
        return tarfile.open(fileobj=io.BytesIO(subprocess.check_output(['ar', 'p', str(path), name])), mode='r:gz')
    with archive('control.tar.gz') as control:
        fields = control.extractfile('./control') or control.extractfile('control')
        metadata = fields.read().decode()
        assert f'Architecture: {arch}\n' in metadata, metadata
        assert 'Package: artex\n' in metadata, metadata
        assert not any(Path(m.name).name in {'preinst', 'postinst', 'prerm', 'postrm'} for m in control)
    with archive('data.tar.gz') as data:
        members = {'/' + m.name.lstrip('./'): m for m in data}
        assert required <= members.keys(), required - members.keys()
        assert members['/usr/bin/artex'].linkname == '/usr/lib/artex/artex'
        assert members['/usr/lib/artex/artex'].mode == 0o755
        desktop = data.extractfile(members['/usr/share/applications/artex.desktop']).read()
        assert b'Exec=/usr/bin/artex launch\n' in desktop
else:
    blob = path.read_bytes()
    def header(offset):
        assert blob[offset:offset + 3] == b'\x8e\xad\xe8'
        count, size = struct.unpack_from('>II', blob, offset + 8)
        start = offset + 16 + count * 16
        values = {}
        for i in range(count):
            tag, kind, pos, length = struct.unpack_from('>IIII', blob, offset + 16 + i * 16)
            if kind in (6, 8, 9):
                values[tag] = blob[start + pos:start + size].split(b'\0')[:length]
            elif kind == 4:
                values[tag] = struct.unpack_from('>' + 'I' * length, blob, start + pos)
        return values, start + size
    _, offset = header(96)
    metadata, _ = header((offset + 7) & ~7)
    assert metadata[1000] == [b'artex']
    assert metadata[1022] == [dict(amd64=b'x86_64', arm64=b'aarch64')[arch]]
    names = [metadata[1118][index].decode() + basename.decode() for index, basename in zip(metadata[1116], metadata[1117])]
    assert required <= set(names), required - set(names)
    assert metadata[1036][names.index('/usr/bin/artex')] == b'/usr/lib/artex/artex'
    assert not any(tag in metadata for tag in (1023, 1024, 1025, 1026)), 'Unexpected package hooks'
print(f'{path.name}: architecture, payload, symlink and hook absence verified')
