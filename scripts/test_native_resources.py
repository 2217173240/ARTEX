import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('stage_skills', Path(__file__).with_name('stage-skills.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ResourcesTest(unittest.TestCase):
    def test_only_tracked_resources_and_executable_modes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            repo, output = root / 'repo', root / 'out'
            repo.mkdir()
            subprocess.run(['git', 'init', '-q', str(repo)], check=True)
            skills = repo / 'skills'
            skills.mkdir()
            (skills / 'SKILL.md').write_text('tracked')
            script = skills / 'run.sh'
            script.write_text('#!/bin/sh\n')
            script.chmod(0o755)
            subprocess.run(['git', '-C', str(repo), 'add', 'skills'], check=True)
            (skills / 'config.json').write_text('private fixture')
            (skills / '.env').write_text('private fixture')
            (skills / 'node_modules').mkdir()
            (skills / 'node_modules' / 'private').write_text('private fixture')
            self.assertEqual(module.stage(repo, output), 2)
            self.assertEqual({p.name for p in output.iterdir()}, {'SKILL.md', 'run.sh'})
            self.assertEqual((output / 'run.sh').stat().st_mode & 0o777, 0o755)


if __name__ == '__main__':
    unittest.main()
