import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]

class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        for name in ['install.sh', 'update.sh', '.env.example']:
            shutil.copy(ROOT / name, self.root / name)
        (self.root / 'scripts').mkdir()
        shutil.copy(ROOT / 'scripts/install-config.py', self.root / 'scripts/install-config.py')
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.stub('docker', 'echo "$*" >> "$FIXTURE_LOG"\ncase "$*" in *pull*) exit "${PULL_STATUS:-0}";; esac')
        self.stub('go', 'case "$*" in version) echo fixture;; *build*) printf "#!/bin/sh\\nexit 0\\n" > artex; chmod +x artex;; esac')
        self.stub('npm', 'mkdir -p out')
        (self.root / 'web').mkdir()
        (self.root / 'server/webui').mkdir(parents=True)
        self.env = dict(os.environ, PATH=str(self.bin) + ':' + os.environ['PATH'], FIXTURE_LOG=str(self.root / 'docker.log'))

    def tearDown(self):
        self.temp.cleanup()

    def stub(self, name, body):
        path = self.bin / name
        path.write_text('#!/bin/sh\n' + body + '\n')
        path.chmod(0o755)

    def run_install(self, answers):
        return subprocess.run(['bash', 'install.sh'], cwd=self.root, env=self.env, input=answers, text=True, capture_output=True)

    def test_pull_failure_never_starts_or_reports_success(self):
        self.env['PULL_STATUS'] = '42'
        result = self.run_install('1\nfixture\nsecret\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('up -d', (self.root / 'docker.log').read_text())
        self.assertNotIn('启动完成', result.stdout)

    def test_docker_secrets_and_permissions(self):
        password = "p'\\$# &: /\""
        key = "key'\\$#&"
        result = self.run_install('1\n' + password + '\n' + key + '\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(password, result.stdout + result.stderr)
        self.assertNotIn(key, result.stdout + result.stderr)
        env = (self.root / '.env').read_text()
        self.assertIn("POSTGRES_PASSWORD='p\\'\\$# &: /\"'", env)
        self.assertIn('p%27%5C%24%23%20%26%3A%20%2F%22', env)
        self.assertEqual((self.root / '.env').stat().st_mode & 0o777, 0o600)
        self.assertIn('up -d', (self.root / 'docker.log').read_text())

    def test_json_escaping_and_existing_configuration(self):
        password = 'quote" slash\\ dollar$ &'
        result = self.run_install('2\n1\n\n\n\n' + password + '\n\n\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        config = self.root / 'config.json'
        self.assertEqual(json.loads(config.read_text())['database']['password'], password)
        self.assertEqual(config.stat().st_mode & 0o777, 0o600)
        original = config.read_bytes()
        result = self.run_install('2\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(config.read_bytes(), original)

    def test_update_pull_failure_and_safe_tag(self):
        (self.root / '.env').write_text('POSTGRES_PASSWORD=fixture\nARTEX_IMAGE=custom/image\n')
        self.env['PULL_STATUS'] = '42'
        result = subprocess.run(['bash', 'update.sh'], cwd=self.root, env=self.env, input='1\nv0.3.17\n', text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('up -d', (self.root / 'docker.log').read_text())
        self.assertNotIn('更新完成', result.stdout)
        self.assertIn("ARTEX_TAG='v0.3.17'", (self.root / '.env').read_text())
        self.assertIn('ARTEX_IMAGE=custom/image', (self.root / '.env').read_text())

    def test_existing_env_preserved(self):
        env = self.root / '.env'
        env.write_text('POSTGRES_PASSWORD=existing\nARTEX_IMAGE=custom/image\n')
        original = env.read_bytes()
        result = self.run_install('1\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(env.read_bytes(), original)

if __name__ == '__main__':
    unittest.main()
