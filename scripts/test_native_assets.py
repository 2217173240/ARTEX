import importlib.util
from pathlib import Path
import tarfile
import tempfile
import unittest
import zipfile


def load(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

assets = load('check-release-assets')
portable = load('package-portable-tar')


class NativeAssetsTests(unittest.TestCase):
    def test_complete_checksums_detect_missing_and_tampered_package(self):
        with tempfile.TemporaryDirectory() as root:
            directory = Path(root)
            self.assertEqual(len(assets.expected('0.3.18')), 16)
            for name in assets.expected('0.3.18'):
                (directory / name).write_bytes(name.encode())
            assets.validate_local(directory, '0.3.18', True)
            assets.validate_local(directory, '0.3.18')
            target = directory / 'artex-0.3.18-windows-arm64.msi'
            target.write_bytes(b'tampered')
            with self.assertRaisesRegex(ValueError, 'Checksum'):
                assets.validate_local(directory, '0.3.18')
            target.unlink()
            with self.assertRaisesRegex(ValueError, 'missing'):
                assets.validate_local(directory, '0.3.18')

    def test_remote_draft_rejects_missing_empty_duplicate_and_published(self):
        names = assets.expected('0.3.18') | {'SHA256SUMS'}
        document = {'isDraft': True, 'assets': [{'name': name, 'size': 1} for name in names]}
        assets.validate_remote(document, '0.3.18')
        for altered in [dict(document, isDraft=False), dict(document, assets=document['assets'][:-1]), dict(document, assets=document['assets'] + [document['assets'][0]]), dict(document, assets=[dict(a, size=0) for a in document['assets']])]:
            with self.assertRaises(ValueError):
                assets.validate_remote(altered, '0.3.18')

    def test_tar_preserves_payload_and_executable_mode(self):
        with tempfile.TemporaryDirectory() as root:
            source, target = Path(root) / 'portable.zip', Path(root) / 'portable.tar.gz'
            with zipfile.ZipFile(source, 'w') as archive:
                archive.writestr('artex-linux-amd64/artex', b'executable')
                archive.writestr('artex-linux-amd64/config.example.json', b'{}')
            portable.convert(source, target)
            with tarfile.open(target) as archive:
                binary = archive.getmember('artex-linux-amd64/artex')
                self.assertEqual(binary.mode, 0o755)
                self.assertEqual(archive.extractfile(binary).read(), b'executable')
            with zipfile.ZipFile(source, 'w') as archive:
                archive.writestr('../escape', b'bad')
            with self.assertRaises(ValueError):
                portable.convert(source, target)

if __name__ == '__main__':
    unittest.main()
