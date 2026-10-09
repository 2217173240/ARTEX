#!/usr/bin/env python3
"""Reject tracked private configuration and runtime output before publication."""
import pathlib
import subprocess
import sys


def private_path(path: str) -> bool:
    parts = pathlib.PurePosixPath(path).parts
    name = parts[-1]
    return (
        name in {"config.json", "jwt.key", ".env", "scheduler_state.json"}
        or (name.startswith(".env.") and name not in {".env.example", ".env.sample"})
        or name.endswith((".db", ".db-wal", ".db-shm", "-events.csv"))
        or any(part in {"node_modules", ".next", "__pycache__"} for part in parts)
        or parts[0] in {"data", "dist", "bench"}
        or path.startswith("server/webui/dist/")
    )


def main() -> int:
    files = subprocess.check_output(["git", "ls-files", "-z"]).decode().split("\0")
    rejected = [path for path in files if path and private_path(path)]
    for path in rejected:
        print(f"Private/runtime path is tracked: {path}", file=sys.stderr)
    if rejected:
        return 1
    print("Tracked publication inputs contain no private/runtime paths.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
