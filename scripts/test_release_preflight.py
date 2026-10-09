import importlib.util
import pathlib
import unittest
import json
import os
import tempfile
from unittest import mock
import urllib.error

spec = importlib.util.spec_from_file_location("preflight", pathlib.Path(__file__).with_name("release-preflight.py"))
preflight = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preflight)


class PreflightTests(unittest.TestCase):
    def test_only_stable_shell_safe_versions(self):
        for tag in ("v0.0.0", "v1.20.300"):
            self.assertTrue(preflight.TAG.fullmatch(tag))
        for tag in ("v01.2.3", "v1.2.3-rc1", "v1.2", "v1.2.3\n", "v1.2.3;true", "$(id)"):
            self.assertFalse(preflight.TAG.fullmatch(tag))

    def test_quality_must_be_exact_commit_and_repository_actions(self):
        check = dict(name="quality", head_sha="abc", app={"slug": "github-actions"},
                     status="completed", conclusion="success",
                     details_url="https://github.com/owner/repo/actions/runs/123/job/456")
        self.assertTrue(preflight.qualified(check, "abc", "owner/repo"))
        for key, value in (("head_sha", "other"), ("name", "backend"),
                           ("app", {"slug": "other"}), ("status", "in_progress"),
                           ("conclusion", "failure"),
                           ("details_url", "https://github.com/other/repo/actions/runs/123")):
            with self.subTest(key=key):
                self.assertFalse(preflight.qualified(dict(check, **{key: value}), "abc", "owner/repo"))

    def test_dispatch_resolves_annotated_tag_not_workflow_head(self):
        check = dict(name="quality", head_sha="tag-commit", app={"slug": "github-actions"},
                     status="completed", conclusion="success",
                     details_url="https://github.com/owner/repo/actions/runs/123")
        paths = []
        def response(request, timeout):
            path = request.full_url.split("/owner/repo", 1)[1].lstrip("/")
            paths.append(path)
            if path == "":
                data = {"default_branch": "main"}
            elif path == "compare/tag-commit...main":
                data = {"merge_base_commit": {"sha": "tag-commit"}}
            elif path.startswith("git/ref/tags/"):
                data = {"object": {"type": "tag", "sha": "annotated"}}
            elif path == "git/tags/annotated":
                data = {"object": {"type": "commit", "sha": "tag-commit"}}
            elif path.startswith("commits/tag-commit/check-runs"):
                data = {"check_runs": [check]}
            elif path == "releases/tags/v1.2.3":
                raise urllib.error.HTTPError(request.full_url, 404, "missing", {}, None)
            else:
                self.fail("Unexpected request: " + path)
            result = mock.MagicMock()
            result.__enter__.return_value.read.return_value = json.dumps(data).encode()
            return result
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "outputs"
            with mock.patch.dict(os.environ, RELEASE_TAG="v1.2.3", GITHUB_REPOSITORY="owner/repo",
                                 GITHUB_TOKEN="synthetic", GITHUB_SHA="workflow-head", GITHUB_OUTPUT=str(output)):
                with mock.patch.object(preflight.urllib.request, "urlopen", side_effect=response):
                    preflight.main()
            self.assertIn("commit=tag-commit", output.read_text())
            self.assertIn("image=ghcr.io/owner/repo", output.read_text())
            self.assertFalse(any("workflow-head" in path for path in paths))


class UnmergedTagTests(unittest.TestCase):
    def test_green_unmerged_tag_cannot_enter_privileged_build(self):
        def response(request, timeout):
            path = request.full_url.split("/owner/repo", 1)[1].lstrip("/")
            if path.startswith("git/ref/tags/"):
                data = {"object": {"type": "commit", "sha": "unmerged"}}
            elif path == "":
                data = {"default_branch": "main"}
            elif path == "compare/unmerged...main":
                data = {"merge_base_commit": {"sha": "older-common-base"}}
            else:
                self.fail("Unmerged code reached privileged release checks: " + path)
            result = mock.MagicMock()
            result.__enter__.return_value.read.return_value = json.dumps(data).encode()
            return result
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "outputs"
            with mock.patch.dict(os.environ, RELEASE_TAG="v1.2.3", GITHUB_REPOSITORY="owner/repo",
                                 GITHUB_TOKEN="synthetic", GITHUB_OUTPUT=str(output)):
                with mock.patch.object(preflight.urllib.request, "urlopen", side_effect=response):
                    with self.assertRaisesRegex(SystemExit, "not been integrated"):
                        preflight.main()
            self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
