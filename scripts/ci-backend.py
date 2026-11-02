import argparse
import os
import subprocess
import time


def packages():
    output = subprocess.run(["go", "list", "./..."], check=True, text=True, stdout=subprocess.PIPE).stdout
    return sorted(output.splitlines())


def partition(items, count):
    groups = [[] for _ in range(count)]
    for index, item in enumerate(items):
        groups[index % count].append(item)
    return groups


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--shard", type=int, required=True)
    parser.add_argument("--shards", type=int, default=3)
    args = parser.parse_args()
    if not 0 <= args.shard < args.shards:
        parser.error("shard must be in [0, shards)")
    selected = partition(packages(), args.shards)[args.shard]
    print(f"Race shard {args.shard + 1}/{args.shards}: {len(selected)} packages", flush=True)
    container = os.environ["POSTGRES_CONTAINER"]
    base = "postgres://artex_ci:fixture_only@127.0.0.1:5432/{}?sslmode=disable"
    for index, package in enumerate(selected, 1):
        database = "artex_ci_%s_%s" % (args.shard, index)
        subprocess.run(["docker", "exec", container, "psql", "-U", "artex_ci", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-c", "CREATE DATABASE %s" % database], check=True)
        dsn = base.format(database)
        env = os.environ.copy()
        for key in ("ARTEX_PG_DSN", "ARTEX_INTERCEPT_TEST_DSN", "ARTEX_AUTH_TEST_DSN", "ARTEX_RATE_TEST_PG_DSN", "ARTEX_LEDGER_TEST_DSN", "ARTEX_COMPANY_TEST_DSN"):
            env[key] = dsn
        started = time.monotonic()
        print(f"Testing {package}", flush=True)
        subprocess.run(["go", "test", "-race", "-count=1", "-skip", "^TestLiveContextReview$", package], check=True, env=env)
        print(f"Package timing: {package} {time.monotonic() - started:.3f}s", flush=True)


if __name__ == "__main__":
    main()
