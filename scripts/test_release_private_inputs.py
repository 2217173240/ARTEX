import contextlib
import importlib.util
import io
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('publication_inputs', Path(__file__).with_name('check-publication-inputs.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class PrivateAuthInputsTest(unittest.TestCase):
    def test_credentials_and_incomplete_private_copies_are_rejected_at_any_depth(self):
        for name in ('setup.token', 'jwt.key', '.auth-private-fixture'):
            for parent in ('', 'skills/custom/'):
                with self.subTest(path=parent + name):
                    self.assertTrue(module.private_path(parent + name))
        for path in ('server/auth_credentials.go', 'docs/setup.token.md', '.env.example'):
            self.assertFalse(module.private_path(path))

    def test_tracked_credential_filename_fails_publication_without_reading_contents(self):
        output = io.StringIO()
        with patch.object(module.subprocess, 'check_output', return_value=b'skills/custom/setup.token\0server/auth_credentials.go\0'):
            with contextlib.redirect_stderr(output):
                self.assertEqual(module.main(), 1)
        self.assertIn('skills/custom/setup.token', output.getvalue())


if __name__ == '__main__':
    unittest.main()
