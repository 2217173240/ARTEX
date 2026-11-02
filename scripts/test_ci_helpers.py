import os
import pathlib
import subprocess
import tempfile
import unittest
import importlib.util

def load(name):
    path = pathlib.Path(__file__).with_name(name + ".py")
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

backend = load("ci-backend")
changes = load("ci-changes")


class CIHelpersTest(unittest.TestCase):
    def setUp(self):
        parent = os.environ.get("ARTEX_CI_TEST_TMP") or os.environ.get("RUNNER_TEMP")
        if not parent:
            self.skipTest("set ARTEX_CI_TEST_TMP locally or RUNNER_TEMP in CI")
        self.temp = tempfile.TemporaryDirectory(dir=parent)
        self.root = pathlib.Path(self.temp.name)
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)
        self.git("config", "user.email", "ci@example.invalid")
        self.git("config", "user.name", "CI fixture")
        (self.root / "README.md").write_text("base\n")
        self.git("add", ".")
        self.git("commit", "-qm", "base")

    def tearDown(self):
        self.temp.cleanup()

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.root, check=True, text=True, stdout=subprocess.PIPE).stdout.strip()

    def run_changes(self, event, output, **values):
        env = os.environ.copy()
        env.update({"GITHUB_EVENT_NAME": event, "GITHUB_OUTPUT": str(output), **values})
        return subprocess.run(["python3", str(pathlib.Path(__file__).with_name("ci-changes.py"))], cwd=self.root, env=env, check=True, text=True, capture_output=True)

    def test_real_git_pr_three_dot_and_github_output(self):
        base = self.git("rev-parse", "HEAD")
        self.git("checkout", "-qb", "base-advance")
        (self.root / "README.md").write_text("advanced\n")
        self.git("add", "README.md")
        self.git("commit", "-qm", "advance base")
        advanced = self.git("rev-parse", "HEAD")
        self.git("checkout", "-qb", "feature", base)
        (self.root / "docs").mkdir()
        (self.root / "docs" / "old.md").write_text("doc\n")
        self.git("add", ".")
        self.git("commit", "-qm", "docs")
        self.git("mv", "docs/old.md", "docs/new.md")
        self.git("commit", "-qm", "rename")
        output = self.root / "github-output"
        result = self.run_changes("pull_request", output, GITHUB_BASE_SHA=advanced, GITHUB_HEAD_SHA=self.git("rev-parse", "HEAD"))
        self.assertIn('"run_ci":false', result.stdout)
        self.assertEqual(output.read_text(), "run_ci=false\n")

        (self.root / "strange name [x].go").write_text("package x\n")
        self.git("add", ".")
        self.git("commit", "-qm", "code")
        output.unlink()
        result = self.run_changes("pull_request", output, GITHUB_BASE_SHA=advanced, GITHUB_HEAD_SHA=self.git("rev-parse", "HEAD"))
        self.assertIn('"run_ci":true', result.stdout)
        self.assertEqual(output.read_text(), "run_ci=true\n")
    def test_path_parser_handles_docs_code_rename_delete_and_unusual_names(self):
        self.assertTrue(changes.is_docs_only(b"M\0docs/guide.md\0"))
        self.assertFalse(changes.is_docs_only(b"M\0src/odd name [x].go\0"))
        self.assertFalse(changes.is_docs_only(b"R100\0docs/old.md\0src/new.go\0"))
        self.assertTrue(changes.is_docs_only(b"D\0docs/removed.md\0"))
        self.assertFalse(changes.is_docs_only(b"A\0docs/example.go\0"))

    def test_partition_is_exact_and_non_overlapping(self):
        items = backend.packages()
        groups = backend.partition(items, 3)
        self.assertEqual(sorted(sum(groups, [])), items)
        self.assertEqual(len(set(sum(groups, []))), len(items))
        self.assertTrue(all(groups))

    def test_dispatch_and_missing_sha_fail_open_to_full(self):
        root = pathlib.Path(__file__).resolve().parents[1]
        env = os.environ.copy()
        env.update({"GITHUB_EVENT_NAME": "workflow_dispatch"})
        result = subprocess.run(["python3", "scripts/ci-changes.py"], cwd=root, env=env, check=True, text=True, capture_output=True)
        self.assertIn('"run_ci":true', result.stdout)
        env.update({"GITHUB_EVENT_NAME": "pull_request", "GITHUB_BASE_SHA": "", "GITHUB_HEAD_SHA": ""})
        result = subprocess.run(["python3", "scripts/ci-changes.py"], cwd=root, env=env, check=True, text=True, capture_output=True)
        self.assertIn('"run_ci":true', result.stdout)


if __name__ == "__main__":
    unittest.main()
