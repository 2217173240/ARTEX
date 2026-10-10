package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLauncherSkillsReconcileUpgradeAndRemoval(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "bundle")
	dst := filepath.Join(root, "user", "skills")
	os.MkdirAll(src, 0700)
	write := func(name, body string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(src, name), []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("update", "v1")
	write("deleted", "v1")
	write("remove", "old")
	write("edited", "old")
	write("edited-remove", "old")
	write("mode-edited", "old")
	if e := launcherSeedSkills(src, dst); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(dst, "edited"), []byte("user"), 0600)
	os.WriteFile(filepath.Join(dst, "edited-remove"), []byte("user"), 0600)
	if runtime.GOOS != "windows" {
		os.Chmod(filepath.Join(dst, "mode-edited"), 0640)
	} else {
		os.Chmod(filepath.Join(dst, "mode-edited"), 0444)
	}
	os.WriteFile(filepath.Join(dst, "addition"), []byte("mine"), 0600)
	os.Remove(filepath.Join(dst, "deleted"))
	write("update", "v2")
	write("deleted", "v2")
	write("edited", "v2")
	write("mode-edited", "v2")
	write("new", "new")
	os.Remove(filepath.Join(src, "remove"))
	os.Remove(filepath.Join(src, "edited-remove"))
	if e := launcherSeedSkills(src, dst); e != nil {
		t.Fatal(e)
	}
	for name, want := range map[string]string{"update": "v2", "edited": "user", "edited-remove": "user", "mode-edited": "old", "addition": "mine", "new": "new"} {
		b, e := os.ReadFile(filepath.Join(dst, name))
		if e != nil || string(b) != want {
			t.Fatalf("%s: %q %v", name, b, e)
		}
	}
	if _, e := os.Stat(filepath.Join(dst, "remove")); !os.IsNotExist(e) {
		t.Fatal("unmodified obsolete file retained")
	}
	if _, e := os.Stat(filepath.Join(dst, "deleted")); !os.IsNotExist(e) {
		t.Fatal("user deletion recreated")
	}
	// A third launch observes the updated manifest, rather than restoring old content.
	if e := launcherSeedSkills(src, dst); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(dst, "update"))
	if string(b) != "v2" {
		t.Fatal("upgrade was not recorded")
	}
}
func TestLauncherSkillsReconcilePreservesUntrackedAndSymlink(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "bundle")
	dst := filepath.Join(root, "user", "skills")
	os.MkdirAll(src, 0700)
	os.MkdirAll(dst, 0700)
	os.WriteFile(filepath.Join(src, "legacy"), []byte("bundle"), 0600)
	os.WriteFile(filepath.Join(dst, "legacy"), []byte("existing"), 0600)
	os.WriteFile(filepath.Join(src, "tracked"), []byte("v1"), 0600)
	if e := launcherSeedSkills(src, dst); e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(root, "outside")
	os.WriteFile(outside, []byte("external"), 0600)
	os.Remove(filepath.Join(dst, "tracked"))
	if e := os.Symlink(outside, filepath.Join(dst, "tracked")); e != nil {
		t.Skip(e)
	}
	os.WriteFile(filepath.Join(src, "tracked"), []byte("v2"), 0600)
	if e := launcherSeedSkills(src, dst); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(outside)
	if string(b) != "external" {
		t.Fatal("symlink target modified")
	}
	st, _ := os.Lstat(filepath.Join(dst, "tracked"))
	if st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("user symlink replaced")
	}
	os.Remove(filepath.Join(src, "tracked"))
	if e := launcherSeedSkills(src, dst); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Lstat(filepath.Join(dst, "tracked")); e != nil {
		t.Fatal("user symlink removed")
	}
	b, _ = os.ReadFile(filepath.Join(dst, "legacy"))
	if string(b) != "existing" {
		t.Fatal("untracked file replaced")
	}
}
