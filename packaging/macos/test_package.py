"""macOS-only package structure checks using tiny Mach-O fixtures, not ARTEX runtime."""
import os
import pathlib
import plistlib
import subprocess
import sys
import tempfile
import unittest
import xml.etree.ElementTree as ET

REPO = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO / "scripts/package-macos.sh"


@unittest.skipUnless(sys.platform == "darwin", "requires native Apple packaging tools")
class PackageTests(unittest.TestCase):
    def test_architecture_payloads(self):
        for arch, macho in (("amd64", "x86_64"), ("arm64", "arm64")):
            with self.subTest(arch=arch), tempfile.TemporaryDirectory() as directory:
                work = pathlib.Path(directory)
                source = work / "main.c"
                source.write_text("int main(void) { return 0; }\n")
                binary = work / "fixture"
                subprocess.run(["clang", "-arch", macho, str(source), "-o", str(binary)], check=True)
                environment = dict(os.environ)
                environment.pop("PKG_SIGNATURE_ID", None)
                result = subprocess.run([
                    str(SCRIPT), "--binary", str(binary), "--version", "0.3.18",
                    "--arch", arch, "--outdir", str(work / "out"),
                ], capture_output=True, text=True, env=environment)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                package = work / "out" / f"artex-0.3.18-darwin-{arch}.pkg"
                expanded = work / "expanded"
                subprocess.run(["pkgutil", "--expand-full", str(package), str(expanded)], check=True)
                info = ET.parse(expanded / "component.pkg/PackageInfo").getroot()
                self.assertEqual(info.attrib["install-location"], "/Applications")
                self.assertEqual(info.attrib["relocatable"], "false")
                self.assertEqual(info.attrib["identifier"], "io.github.2217173240.artex")
                self.assertIsNotNone(info.find("upgrade-bundle/bundle"))
                self.assertFalse((expanded / "component.pkg/Scripts").exists())
                distribution = ET.parse(expanded / "Distribution").getroot()
                self.assertEqual(distribution.find("options").attrib["hostArchitectures"], macho)
                app = expanded / "component.pkg/Payload/ARTEX.app"
                with (app / "Contents/Info.plist").open("rb") as stream:
                    bundle = plistlib.load(stream)
                self.assertEqual(bundle["CFBundleExecutable"], "artex")
                self.assertEqual(bundle["CFBundleIdentifier"], "io.github.2217173240.artex")
                self.assertEqual(bundle["CFBundleVersion"], "0.3.18")
                self.assertTrue((app / "Contents/Resources/ARTEX.icns").is_file())
                self.assertTrue((app / "Contents/Resources/skills/api-recon/SKILL.md").is_file())
                self.assertFalse(list(app.rglob("config.json")))
                self.assertFalse(list(app.rglob(".env")))
                self.assertEqual(subprocess.check_output([
                    "lipo", "-archs", str(app / "Contents/MacOS/artex")
                ], text=True).strip(), macho)
                wrong_arch = "arm64" if arch == "amd64" else "amd64"
                rejected = subprocess.run([
                    str(SCRIPT), "--binary", str(binary), "--version", "0.3.18",
                    "--arch", wrong_arch, "--outdir", str(work / "rejected"),
                ], capture_output=True, text=True)
                self.assertNotEqual(rejected.returncode, 0)
                self.assertIn("Binary must contain only", rejected.stderr)


if __name__ == "__main__":
    unittest.main()
