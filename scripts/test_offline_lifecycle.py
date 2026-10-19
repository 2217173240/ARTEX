import argparse
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import time
import uuid


def run(*args, **kwargs):
    return subprocess.check_output(args, text=True, **kwargs).strip()


def main():
    parser = argparse.ArgumentParser(description="Real install/reinstall acceptance in a network-disabled disposable Docker engine")
    parser.add_argument("bundle", type=Path)
    args = parser.parse_args()
    bundle = args.bundle.resolve()
    if not bundle.is_file():
        raise ValueError("Expected an existing offline archive")
    name = "artex-offline-test-" + uuid.uuid4().hex[:12]
    image = name + ":harness"
    # 验收镜像只提供目标机的 Docker、Compose、Bash；应用来自待测离线包。
    subprocess.run(["docker", "build", "-t", image, "-"], input=
        "FROM docker:28-dind\nRUN apk add --no-cache bash\n", text=True, check=True)
    with tempfile.TemporaryDirectory(prefix=".offline-test-", dir=bundle.parent) as temporary:
        root = Path(temporary)
        with tarfile.open(bundle) as archive:
            archive.extractall(root, filter="data")
        directories = list(root.iterdir())
        if len(directories) != 1 or not directories[0].is_dir():
            raise ValueError("Bundle must contain exactly one root directory")
        stage = directories[0]
        target = "/offline/" + stage.name
        started = False
        try:
            subprocess.run(["docker", "run", "--detach", "--privileged", "--network", "none", "--name", name,
                "--env", "DOCKER_TLS_CERTDIR=", "--volume", f"{root}:/offline", image,
                "dockerd", "--host=unix:///var/run/docker.sock"], check=True)
            started = True
            def inside(*command):
                return run("docker", "exec", "--workdir", target, name, *command)
            for _ in range(60):
                ready = subprocess.run(["docker", "exec", name, "docker", "info"], capture_output=True)
                if ready.returncode == 0:
                    break
                time.sleep(1)
            else:
                raise RuntimeError("Disposable Docker engine did not start")
            assert run("docker", "inspect", "--format", "{{.HostConfig.NetworkMode}}", name) == "none"
            assert json.loads(inside("docker", "image", "ls", "--format", "json") or "{}") == {}
            # 首先验证损坏输入在创建配置或载入镜像前被拒绝。
            inventory = stage / "IMAGES"
            original = inventory.read_bytes()
            inventory.write_bytes(original + b"corrupt\n")
            damaged = subprocess.run(["docker", "exec", "--workdir", target, name, "bash", "./artex-offline", "install"], capture_output=True)
            assert damaged.returncode != 0 and not (stage / ".env").exists()
            inventory.write_bytes(original)
            checksums = stage / "SHA256SUMS"
            original_checksums = checksums.read_text()
            entries = original_checksums.splitlines()
            # 合法路径重复不得掩盖遗漏的应用镜像校验行。
            duplicate = next(line for line in entries if line.endswith("  IMAGES"))
            checksums.write_text("\n".join(duplicate if line.endswith("  artex.tar") else line for line in entries) + "\n")
            damaged = subprocess.run(["docker", "exec", "--workdir", target, name, "bash", "./artex-offline", "install"], capture_output=True)
            assert damaged.returncode != 0 and not (stage / ".env").exists()
            checksums.write_text(original_checksums)
            print(inside("bash", "./artex-offline", "install"))
            env_hash = inside("sha256sum", ".env").split()[0]
            project = inside("cat", ".project")
            compose = ("docker", "compose", "-p", project, "-f", "compose.yaml", "--env-file", ".env")
            health = inside(*compose, "exec", "-T", "artex", "curl", "--fail", "--silent", "http://127.0.0.1:8787/api/health")
            assert json.loads(health)["ok"] is True
            version = json.loads((stage / "manifest.json").read_text())["version"].removeprefix("v")
            assert json.loads(health)["version"] == version
            inside(*compose, "exec", "-T", "postgres", "psql", "-U", "artex", "-d", "artex", "-v", "ON_ERROR_STOP=1", "-c",
                   "CREATE TABLE offline_preservation (value text); INSERT INTO offline_preservation VALUES ('preserved');")
            inside("sh", "-c", "printf preserved > data/offline-sentinel")
            print(inside("bash", "./artex-offline", "stop"))
            # 删除容器并保留卷，验证 install 恢复真实持久化数据。
            inside(*compose, "down")
            for line in original.decode().splitlines():
                inside("docker", "image", "rm", line.split()[1])
            print(inside("bash", "./artex-offline", "install"))
            assert inside("sha256sum", ".env").split()[0] == env_hash
            assert inside("cat", ".project") == project
            assert inside("cat", "data/offline-sentinel") == "preserved"
            value = inside(*compose, "exec", "-T", "postgres", "psql", "-U", "artex", "-d", "artex", "-Atc", "SELECT value FROM offline_preservation")
            assert value == "preserved"
            for filename in (".env", ".project"):
                assert inside("stat", "-c", "%a", filename) == "600"
            assert inside("stat", "-c", "%a", "data") == "700"
            print(inside("bash", "./artex-offline", "status"))
            print("PASS: network=none, empty image store, corruption/duplicate-inventory rejection, install, health/version, reinstall, SQL/file/config preservation, permissions")
        finally:
            if started:
                subprocess.run(["docker", "logs", name], check=False)
                # 仅清理本次解压目录；允许普通 CI 用户回收容器创建的私有目录。
                subprocess.run(["docker", "exec", name, "rm", "-rf", target], check=True)
                subprocess.run(["docker", "rm", "--force", "--volumes", name], check=True)
            subprocess.run(["docker", "image", "rm", image], check=True)


if __name__ == "__main__":
    main()
