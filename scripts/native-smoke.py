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


def expect_forbidden(opener, url, headers):
    """Require an HTTP 403, retrying only a reset before a response arrives."""
    boundary = '/'.join(headers)
    for attempt in range(1, 4):
        request = urllib.request.Request(url, headers=headers, data=b'dsn=invalid', method='POST')
        try:
            with opener.open(request, timeout=5):
                raise AssertionError(f'{boundary} boundary accepted')
        except urllib.error.HTTPError as error:
            with error:
                if error.code != 403:
                    raise AssertionError(f'{boundary} boundary returned HTTP {error.code}, expected 403')
            return
        except (ConnectionResetError, urllib.error.URLError) as error:
            reason = error.reason if isinstance(error, urllib.error.URLError) else error
            if not isinstance(reason, ConnectionResetError):
                raise
            if attempt == 3:
                raise RuntimeError(f'{boundary} boundary received no HTTP response after 3 connection resets') from error
            time.sleep(.1)


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
                expect_forbidden(opener, url, headers)
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
