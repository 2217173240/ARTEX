import argparse
import json
import os
import subprocess
import time

TIMINGS_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "package-timings.json")


def packages():
    output = subprocess.run(["go", "list", "./..."], check=True, text=True, stdout=subprocess.PIPE).stdout
    return sorted(output.splitlines())


def load_timings():
    try:
        with open(TIMINGS_PATH, encoding="utf-8") as handle:
            data = json.load(handle)
    except (OSError, ValueError):
        data = {}
    if not isinstance(data, dict):
        data = {}
    default = float(data.get("_meta", {}).get("default_estimate", 3.0))
    table = {}
    for name, value in data.items():
        if name.startswith("_") or not isinstance(value, (int, float)):
            continue
        table[name] = float(value)
    return table, default


def partition(items, count, timings=None, default_estimate=3.0):
    """按实测时长做 LPT(最长处理时间优先)均衡分片。

    未知包按 default_estimate 估(新包略偏保守,避免低估造成隐性热点);
    无时长数据时退回轮转分片。排序与并列都按包名字典序,保证确定性:
    同一份包列表在任何机器上分出同一结果。
    """
    groups = [[] for _ in range(count)]
    loads = [0.0] * count
    if not timings:
        for index, item in enumerate(items):
            groups[index % count].append(item)
        return groups
    for item in sorted(items, key=lambda name: (-timings.get(name, default_estimate), name)):
        target = min(range(count), key=lambda index: (loads[index], index))
        groups[target].append(item)
        loads[target] += timings.get(item, default_estimate)
    return groups


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--shard", type=int, required=True)
    parser.add_argument("--shards", type=int, default=3)
    args = parser.parse_args()
    if not 0 <= args.shard < args.shards:
        parser.error("shard must be in [0, shards)")
    timings, default_estimate = load_timings()
    selected = partition(packages(), args.shards, timings, default_estimate)[args.shard]
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
