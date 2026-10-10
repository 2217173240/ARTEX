#!/usr/bin/env python3
"""Exercise a native launcher with isolated first-run state and no model calls."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def smoke(binary):
    binary = str(Path(binary).resolve())
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with tempfile.TemporaryDirectory(prefix='artex-native-smoke-') as home:
        env = dict(os.environ, ARTEX_HOME=home, ARTEX_CONFIG=str(Path(home) / 'config.json'), ARTEX_LAUNCH_NO_BROWSER='1')
        for key in list(env):
            if key.startswith('ARTEX_PG_') or key == 'PG_DSN':
                env.pop(key)
        Path(env['ARTEX_CONFIG']).write_text('{}')
        def run(*args):
            return subprocess.run([binary, 'launch', *args], env=env, text=True, encoding='utf-8', capture_output=True, timeout=45, check=True)
        try:
            run('--no-browser')
            deadline = time.monotonic() + 30
            while True:
                result = subprocess.run([binary, 'launch', 'status'], env=env, text=True, encoding='utf-8', capture_output=True, timeout=10)
                if result.returncode == 0:
                    status = json.loads(result.stdout)
                    break
                if time.monotonic() > deadline:
                    raise RuntimeError('Launcher status timed out')
                time.sleep(.25)
            assert {'url', 'phase', 'message', 'log'} <= set(status) <= {'url', 'phase', 'message', 'log', 'control_url'}, status
            assert status['phase'] == 'setup', status
            url = status['url']
            assert url.startswith('http://127.0.0.1:'), url
            with opener.open(url, timeout=5) as response:
                assert b'PostgreSQL' in response.read()
            for headers in [{'Host': 'attacker.invalid'}, {'Origin': 'https://attacker.invalid'}]:
                request = urllib.request.Request(url, headers=headers, data=b'dsn=invalid', method='POST')
                try:
                    opener.open(request, timeout=5)
                    raise AssertionError('Host/Origin boundary accepted')
                except urllib.error.HTTPError as error:
                    assert error.code == 403, error.code
        finally:
            run('stop')
        deadline = time.monotonic() + 10
        while Path(home, 'launcher.json').exists():
            if time.monotonic() > deadline:
                raise RuntimeError('Launcher failed to stop')
            time.sleep(.1)
        print('Native first-run setup, sanitized status, Host/Origin boundary and graceful stop passed')

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary')
    smoke(parser.parse_args().binary)
