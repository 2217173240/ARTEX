#!/usr/bin/env python3
"""Installer serialization; secrets arrive through stdin, never argv or logs."""
import json
import os
from pathlib import Path
import sys
from urllib.parse import quote


def env_quote(value):
    return "'" + value.replace("'", "\\'") + "'"


def update_env(values, create=False):
    target = Path('.env')
    source = Path('.env.example') if create else target
    lines = source.read_text().splitlines()
    for key, value in values.items():
        replacement = key + '=' + env_quote(value)
        lines = [line for line in lines if not line.startswith(key + '=')]
        lines.append(replacement)
    with target.open('x' if create else 'w') as output:
        output.write('\n'.join(lines) + '\n')
    target.chmod(0o600)


def main():
    os.umask(0o077)
    values = sys.stdin.buffer.read().decode().split('\0')[:-1]
    mode = sys.argv[1]
    if mode == 'env':
        password, key = values
        dsn = 'postgres://artex:' + quote(password, safe='') + '@postgres:5432/artex?sslmode=disable'
        update_env({'POSTGRES_PASSWORD': password, 'ANTHROPIC_API_KEY': key, 'ARTEX_PG_DSN': dsn}, create=True)
    elif mode == 'json':
        host, port, user, password, dbname, sslmode = values
        port = int(port)
        if not 1 <= port <= 65535:
            raise ValueError('invalid database port')
        target = Path('config.json')
        with target.open('x') as output:
            json.dump({'database': dict(host=host, port=port, user=user, password=password, dbname=dbname, sslmode=sslmode)}, output, ensure_ascii=False, indent=2)
            output.write('\n')
    elif mode == 'tag':
        update_env({'ARTEX_TAG': values[0]})
    else:
        raise ValueError('unknown mode')


if __name__ == '__main__':
    main()
