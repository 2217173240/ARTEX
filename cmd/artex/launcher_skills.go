package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type launcherSkillStamp struct {
	Hash string `json:"sha256"`
	Mode uint32 `json:"mode"`
}
type launcherSkillManifest map[string]launcherSkillStamp

func launcherSkillHash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func launcherSkillPath(root, rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) || filepath.ToSlash(filepath.Clean(rel)) != rel || rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "\\") {
		return "", false
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		st, e := os.Lstat(parent)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return "", false
		}
		if parent == root {
			break
		}
		if parent == filepath.Dir(parent) {
			return "", false
		}
	}
	return path, true
}
func launcherSkillMatches(path string, want launcherSkillStamp) bool {
	st, e := os.Lstat(path)
	if e != nil || !st.Mode().IsRegular() || uint32(st.Mode().Perm()) != want.Mode {
		return false
	}
	b, e := os.ReadFile(path)
	return e == nil && launcherSkillHash(b) == want.Hash
}
func launcherSkillAtomic(path string, b []byte, mode os.FileMode) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".artex-skill-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), path)
}

// Reconcile only files whose content and mode still equal the last seeded version.
// Files predating the manifest and user additions remain outside managed ownership.
func launcherSeedSkills(src, dst string) error {
	source, e := os.Lstat(src)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !source.IsDir() || source.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	dst, e = filepath.Abs(dst)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(dst, 0700); e != nil {
		return e
	}
	st, e := os.Lstat(dst)
	if e != nil {
		return e
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	manifestPath := filepath.Join(filepath.Dir(dst), ".launcher-skills.json")
	previous := launcherSkillManifest{}
	if st, e := os.Lstat(manifestPath); e == nil {
		if !st.Mode().IsRegular() {
			return fmt.Errorf("skill manifest is not a regular file")
		}
		b, e := os.ReadFile(manifestPath)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(b, &previous); e != nil {
			return fmt.Errorf("invalid skill manifest: %w", e)
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	next := launcherSkillManifest{}
	bundled := map[string]bool{}
	e = filepath.WalkDir(src, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, e := filepath.Rel(src, path)
		if e != nil {
			return e
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			target := filepath.Join(dst, filepath.FromSlash(rel))
			if _, safe := launcherSkillPath(dst, rel); !safe {
				return filepath.SkipDir
			}
			if st, e := os.Lstat(target); e == nil {
				if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
					return filepath.SkipDir
				}
				return nil
			} else if !os.IsNotExist(e) {
				return e
			}
			return os.Mkdir(target, 0700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		bundled[rel] = true
		target, safe := launcherSkillPath(dst, rel)
		if !safe {
			return nil
		}
		content, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		mode := os.FileMode(0600) | st.Mode()&0100
		if runtime.GOOS == "windows" {
			mode = 0666
		}
		stamp := launcherSkillStamp{launcherSkillHash(content), uint32(mode)}
		old, tracked := previous[rel]
		if _, e := os.Lstat(target); e == nil {
			if !tracked {
				return nil
			}
			next[rel] = old
			if !launcherSkillMatches(target, old) {
				return nil
			}
			if old != stamp {
				if e = launcherSkillAtomic(target, content, mode); e != nil {
					return e
				}
			}
			next[rel] = stamp
			return nil
		} else if !os.IsNotExist(e) {
			return e
		}
		if tracked {
			// A user deletion is an edit too; do not recreate a managed file.
			next[rel] = old
			return nil
		}
		out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if os.IsExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		_, e = out.Write(content)
		if e == nil {
			e = out.Chmod(mode)
		}
		ce := out.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		next[rel] = stamp
		return nil
	})
	if e != nil {
		return e
	}
	for rel, old := range previous {
		if bundled[rel] {
			continue
		}
		target, safe := launcherSkillPath(dst, rel)
		if safe && launcherSkillMatches(target, old) {
			if e = os.Remove(target); e != nil {
				return e
			}
		}
	}
	b, e := json.MarshalIndent(next, "", "  ")
	if e != nil {
		return e
	}
	return launcherSkillAtomic(manifestPath, b, 0600)
}
