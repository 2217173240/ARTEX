import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
import base64

ROOT = Path(__file__).resolve().parents[1]
TAG = re.compile(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\Z")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def api(repository, path):
    headers = {"Accept": "application/vnd.github+json"}
    if os.environ.get("GITHUB_TOKEN"):
        headers["Authorization"] = "Bearer " + os.environ["GITHUB_TOKEN"]
    request = urllib.request.Request(f"https://api.github.com/repos/{repository}/{path}", headers=headers)
    with urllib.request.urlopen(request, timeout=60) as response:
        return json.load(response)


def release_identity(repository, version):
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository) or not TAG.fullmatch(version):
        raise ValueError("Expected repository owner/name and stable vMAJOR.MINOR.PATCH version")
    release = api(repository, "releases/tags/" + version)
    if release["draft"] or release["prerelease"] or release["tag_name"] != version:
        raise ValueError("A public stable release is required")
    obj = api(repository, "git/ref/tags/" + version)["object"]
    for _ in range(16):
        if obj["type"] == "commit":
            break
        if obj["type"] != "tag":
            raise ValueError("Release tag must resolve to a commit")
        obj = api(repository, "git/tags/" + obj["sha"])["object"]
    else:
        raise ValueError("Too many nested tags")
    assets = [asset for asset in release["assets"] if asset["name"] == "IMAGE_DIGEST"]
    if len(assets) != 1:
        raise ValueError("Release requires exactly one IMAGE_DIGEST asset")
    url = assets[0]["browser_download_url"]
    expected = f"https://github.com/{repository}/releases/download/{version}/IMAGE_DIGEST"
    if url != expected:
        raise ValueError("Unexpected release asset URL")
    with urllib.request.urlopen(url, timeout=60) as response:
        image = response.read(512).decode().strip()
    prefix = f"ghcr.io/{repository.lower()}@"
    if not image.startswith(prefix) or not DIGEST.fullmatch(image[len(prefix):]):
        raise ValueError("IMAGE_DIGEST must identify this repository's immutable GHCR image")
    return obj["sha"], image


def platform_image(reference, arch):
    repository, digest = reference.split("@", 1)
    # 同时存在 tag 与 digest 时，去除可变 tag，再交给 manifest CLI。
    if ":" in repository.rsplit("/", 1)[-1]:
        repository = repository.rsplit(":", 1)[0]
    manifest = json.loads(run("docker", "manifest", "inspect", repository + "@" + digest))
    candidates = [item["digest"] for item in manifest.get("manifests", [])
                  if item.get("platform", {}).get("os") == "linux"
                  and item["platform"].get("architecture") == arch]
    if len(candidates) != 1 or not DIGEST.fullmatch(candidates[0]):
        raise ValueError(f"Expected exactly one linux/{arch} manifest in {reference}")
    return repository + "@" + candidates[0]


def hash_file(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def compose(artex, postgres):
    return f'''services:
  postgres:
    image: {postgres}
    pull_policy: never
    environment:
      POSTGRES_USER: artex
      POSTGRES_DB: artex
      POSTGRES_PASSWORD: ${{POSTGRES_PASSWORD:?Run ./artex-offline install}}
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: [CMD-SHELL, "pg_isready -U artex -d artex"]
      interval: 2s
      timeout: 3s
      retries: 30
    restart: unless-stopped
  artex:
    image: {artex}
    pull_policy: never
    depends_on:
      postgres:
        condition: service_healthy
    environment:
      ARTEX_CONFIG: /dev/null
      ARTEX_PG_DSN: postgres://artex:${{POSTGRES_PASSWORD}}@postgres:5432/artex?sslmode=disable
    ports:
      - "127.0.0.1:${{ARTEX_PORT:-8787}}:8787"
      - "127.0.0.1:${{ARTEX_PROXY_PORT:-8788}}:8788"
    volumes:
      - ./data:/app/data
    healthcheck:
      test: [CMD, curl, --fail, --silent, http://127.0.0.1:8787/api/health]
      interval: 3s
      timeout: 3s
      retries: 40
    restart: unless-stopped
volumes:
  pgdata:
'''


def build(args):
    revision, release_image = release_identity(args.repository, args.version)
    if not re.fullmatch(r"postgres:16\.[0-9]+-alpine@sha256:[a-f0-9]{64}", args.postgres):
        raise ValueError("--postgres requires an exact PostgreSQL 16 minor tag and immutable index digest")
    run("docker", "info")
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    name = f"artex-{args.version}-linux-{args.arch}-offline"
    final = output / (name + ".tar.gz")
    if final.exists() or final.with_suffix(final.suffix + ".sha256").exists():
        raise ValueError("Output already exists; choose a new output directory")
    # 临时目录只包含明确列出的交付文件。
    with tempfile.TemporaryDirectory(prefix=".offline-", dir=output) as temporary:
        stage = Path(temporary) / name
        stage.mkdir(mode=0o700)
        images = []
        for service, reference in (("artex", release_image), ("postgres", args.postgres)):
            pinned = platform_image(reference, args.arch)
            subprocess.run(["docker", "pull", "--platform", f"linux/{args.arch}", pinned], check=True)
            info = json.loads(run("docker", "image", "inspect", pinned))[0]
            config_digest = json.loads(run("docker", "manifest", "inspect", pinned))["config"]["digest"]
            manifest_digest = pinned.split("@", 1)[1]
            if not DIGEST.fullmatch(config_digest) or info["Id"] not in (config_digest, manifest_digest):
                raise ValueError("Local image identity does not match the published manifest/config")
            if info["Os"] != "linux" or info["Architecture"] != args.arch:
                raise ValueError("Pulled image platform mismatch")
            if service == "artex":
                labels = info["Config"].get("Labels") or {}
                required = {"org.opencontainers.image.revision": revision,
                            "org.opencontainers.image.version": args.version,
                            "org.opencontainers.image.source": "https://github.com/" + args.repository,
                            "org.opencontainers.image.licenses": "AGPL-3.0"}
                if any(labels.get(key) != value for key, value in required.items()):
                    raise ValueError("Published ARTEX image labels do not match the release")
            else:
                expected_minor = args.postgres.split(":", 1)[1].split("-", 1)[0]
                if not {"PG_MAJOR=16", "PG_VERSION=" + expected_minor}.issubset(info["Config"].get("Env", [])):
                    raise ValueError("Database image version must match the selected PostgreSQL 16 minor tag")
            local = f"artex-offline-{service}:{args.version[1:]}-{args.arch}-{config_digest[7:23]}"
            subprocess.run(["docker", "tag", pinned, local], check=True)
            subprocess.run(["docker", "save", "--output", str(stage / (service + ".tar")), local], check=True)
            images.append({"service": service, "reference": pinned, "index": reference,
                           "id": config_digest, "manifest_digest": manifest_digest,
                           "local": local, "architecture": args.arch})
        (stage / "IMAGES").write_text("".join(f"{item['id']} {item['local']} {args.arch} {item['manifest_digest']}\n" for item in images))
        (stage / "compose.yaml").write_text(compose(images[0]["local"], images[1]["local"]))
        (stage / "manifest.json").write_text(json.dumps({"schema": 1, "version": args.version,
            "repository": args.repository, "source_revision": revision, "images": images,
            "source_url": f"https://github.com/{args.repository}/tree/{revision}"}, indent=2) + "\n")
        (stage / ".env.example").write_text("# install generates a random password; preserve .env after installation.\nPOSTGRES_PASSWORD=\nARTEX_PORT=8787\nARTEX_PROXY_PORT=8788\n")
        for source, destination in ((ROOT / "scripts/artex-offline", "artex-offline"),
                                    (ROOT / "docs/offline-deployment.md", "README.md")):
            shutil.copyfile(source, stage / destination)
        for filename in ("LICENSE", "THIRD-PARTY-NOTICES.txt"):
            content = api(args.repository, f"contents/{filename}?ref={revision}")
            if content.get("encoding") != "base64" or content.get("type") != "file":
                raise ValueError("Release license metadata must be a regular source file")
            (stage / filename).write_bytes(base64.b64decode(content["content"], validate=False))
        for file in stage.iterdir():
            file.chmod(0o700 if file.name == "artex-offline" else 0o600)
        (stage / "SHA256SUMS").write_text("".join(f"{hash_file(file)}  {file.name}\n" for file in sorted(stage.iterdir())))
        (stage / "SHA256SUMS").chmod(0o600)
        archive = Path(temporary) / final.name
        with tarfile.open(archive, "w:gz") as target:
            target.add(stage, arcname=name)
        # 同目录硬链接以独占方式发布完整归档，保留并发构建的既有结果。
        os.link(archive, final)
        with final.with_suffix(final.suffix + ".sha256").open("x") as checksum:
            checksum.write(f"{hash_file(final)}  {final.name}\n")
    print(final)


def main():
    if sys.version_info < (3, 11):
        raise SystemExit("Offline packaging requires Python 3.11 or newer")
    parser = argparse.ArgumentParser(description="Package verified published images on an online Docker host")
    parser.add_argument("--version", required=True)
    parser.add_argument("--arch", required=True, choices=("amd64", "arm64"))
    parser.add_argument("--repository", default="2217173240/ARTEX")
    parser.add_argument("--postgres", required=True, help="postgres:16.MINOR-alpine@sha256:INDEX_DIGEST")
    parser.add_argument("--output", default="dist/offline")
    build(parser.parse_args())


if __name__ == "__main__":
    main()
