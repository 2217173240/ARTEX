#!/usr/bin/env python3
"""Resolve an existing stable tag and require repository-owned quality evidence."""
import json
import os
import re
import urllib.error
import urllib.parse
import urllib.request

TAG = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z")


def qualified(check, sha, repository):
    return (check.get("name") == "quality" and check.get("head_sha") == sha
            and check.get("app", {}).get("slug") == "github-actions"
            and check.get("status") == "completed" and check.get("conclusion") == "success"
            and re.fullmatch(r"https://github\.com/" + re.escape(repository)
                             + r"/actions/runs/[0-9]+(?:/job/[0-9]+)?", check.get("details_url", "")) is not None)


def main():
    tag = os.environ["RELEASE_TAG"]
    if not TAG.fullmatch(tag):
        raise SystemExit("Release tag must be an existing stable vMAJOR.MINOR.PATCH tag.")
    repo = os.environ["GITHUB_REPOSITORY"]
    def api(path):
        request = urllib.request.Request(f"https://api.github.com/repos/{repo}/{path}", headers={
            "Authorization": "Bearer " + os.environ["GITHUB_TOKEN"],
            "Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2026-03-10"})
        with urllib.request.urlopen(request, timeout=30) as response:
            return json.load(response)
    obj = api("git/ref/tags/" + urllib.parse.quote(tag, safe=""))["object"]
    for _ in range(16):
        if obj["type"] == "commit":
            break
        if obj["type"] != "tag":
            raise SystemExit("Tag does not resolve to a commit.")
        obj = api("git/tags/" + obj["sha"])["object"]
    else:
        raise SystemExit("Too many nested annotated tags.")
    sha = obj["sha"]
    # A successful PR build alone does not authorize executing its code with
    # release privileges. The tag must be an ancestor of the default branch.
    metadata = api("")
    default_branch = metadata["default_branch"]
    comparison = api("compare/" + sha + "..." + urllib.parse.quote(default_branch, safe=""))
    if comparison["merge_base_commit"]["sha"] != sha:
        raise SystemExit("Release tag commit has not been integrated into the default branch.")
    for page in range(1, 1001):
        checks = api(f"commits/{sha}/check-runs?per_page=100&page={page}")["check_runs"]
        if any(qualified(check, sha, repo) for check in checks):
            break
        if len(checks) < 100:
            raise SystemExit("Exact tag commit lacks successful repository GitHub Actions quality evidence.")
    else:
        raise SystemExit("Quality check pagination exceeded safe limit.")
    try:
        release = api("releases/tags/" + tag)
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise
    else:
        if not release["draft"]:
            raise SystemExit("Release is already public; use a new version tag.")
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"tag={tag}\ncommit={sha}\nimage=ghcr.io/{repo.lower()}\n")


if __name__ == "__main__":
    main()
