import json
import os
import subprocess
import sys
import re

SHA = re.compile(r"^[0-9a-fA-F]{40}(?:[0-9a-fA-F]{24})?$")


def git(*args):
    try:
        return subprocess.run(["git", *args], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout
    except subprocess.CalledProcessError as error:
        print(error.stderr.decode(errors="replace"), file=sys.stderr, end="")
        return None


def changed_paths(event):
    if event == "pull_request":
        base = os.environ.get("GITHUB_BASE_SHA")
        head = os.environ.get("GITHUB_HEAD_SHA") or os.environ.get("GITHUB_SHA")
        if not base or not head or not SHA.fullmatch(base) or not SHA.fullmatch(head):
            return None
        return git("diff", "--name-status", "--find-renames", "--find-copies", "-z", f"{base}...{head}", "--")
    if event == "push":
        before = os.environ.get("GITHUB_EVENT_BEFORE")
        after = os.environ.get("GITHUB_SHA")
        if not before or not after or set(before) == {"0"} or not SHA.fullmatch(before) or not SHA.fullmatch(after):
            return None
        return git("diff", "--name-status", "--find-renames", "--find-copies", "-z", before, after, "--")
    return None


def is_docs_only(data):
    fields = data.split(b"\0")
    paths = []
    index = 0
    while index < len(fields) - 1:
        status = fields[index].decode(errors="replace")
        index += 1
        count = 2 if status[:1] in "RC" else 1
        paths.extend(item.decode(errors="replace") for item in fields[index:index + count])
        index += count
    if not paths:
        return False
    return all(path == "README.md" or (path.startswith("docs/") and path.endswith(".md")) for path in paths)


def main():
    event = os.environ.get("GITHUB_EVENT_NAME", "")
    result = {"run_ci": True}
    if event == "workflow_dispatch":
        pass
    else:
        diff = changed_paths(event)
        result["run_ci"] = True if diff is None else not is_docs_only(diff)
    encoded = json.dumps(result, separators=(",", ":"))
    print(encoded)
    output = os.environ.get("GITHUB_OUTPUT")
    if output:
        with open(output, "a", encoding="utf-8") as stream:
            stream.write("run_ci=%s\n" % str(result["run_ci"]).lower())


if __name__ == "__main__":
    main()
