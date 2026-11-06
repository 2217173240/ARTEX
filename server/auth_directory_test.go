package server

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAuthStatePrecedenceAndReload(t *testing.T) {
	for _, source := range []string{"primary", "base", "data", "generate"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			primary, base, data := filepath.Join(root, "auth"), filepath.Join(root, "base"), filepath.Join(root, "data")
			for _, dir := range []string{base, data} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			expected := bytes.Repeat([]byte("a"), 32)
			for name, dir := range map[string]string{"primary": primary, "base": base, "data": data} {
				if source == "generate" || (source == "base" && name == "primary") || (source == "data" && name != "data") {
					continue
				}
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				value := bytes.Repeat([]byte(name[:1]), 32)
				if name == source {
					expected = value
				}
				if err := os.WriteFile(filepath.Join(dir, jwtKeyFilename), value, 0600); err != nil {
					t.Fatal(err)
				}
			}
			key, err := loadOrCreateJWTKey(primary, data, base)
			if err != nil {
				t.Fatal(err)
			}
			if source != "generate" && !bytes.Equal(key, expected) {
				t.Fatal("wrong precedence")
			}
			again, err := loadOrCreateJWTKey(primary, data, base)
			if err != nil || !bytes.Equal(key, again) {
				t.Fatal("reload changed key", err)
			}
			before, after := &Server{jwtKey: key}, &Server{jwtKey: again}
			token, err := before.signAdminJWT("stored-password-hash")
			if err != nil || !after.verifyAdminJWT(token, "stored-password-hash") || after.verifyAdminJWT(token, "changed-password-hash") {
				t.Fatal("session continuity or revocation failed", err)
			}
			info, err := os.Stat(primary)
			if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0700) {
				t.Fatal("directory permissions", err)
			}
		})
	}
}

func TestAuthStateDirectoryGuard(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{workspace, filepath.Join(workspace, "missing", "auth")} {
		if err := validateAuthDirectory(path, workspace); err == nil {
			t.Fatalf("accepted workspace auth path %s", path)
		}
	}
	for _, path := range []string{root, filepath.Join(root, "auth")} {
		if err := validateAuthDirectory(path, workspace); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("symlink", func(t *testing.T) {
		alias := filepath.Join(root, "alias")
		if err := os.Symlink(workspace, alias); err != nil {
			t.Skip(err)
		}
		for _, path := range []string{alias, filepath.Join(alias, "missing")} {
			if err := validateAuthDirectory(path, workspace); err == nil {
				t.Fatalf("accepted alias %s", path)
			}
		}
	})
}

func TestAuthStateInvalidPrimaryPreserved(t *testing.T) {
	primary, legacy := t.TempDir(), t.TempDir()
	path := filepath.Join(primary, jwtKeyFilename)
	invalid := []byte("invalid")
	if err := os.WriteFile(path, invalid, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, jwtKeyFilename), bytes.Repeat([]byte("l"), 32), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateJWTKey(primary, legacy); err == nil {
		t.Fatal("invalid primary accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, invalid) {
		t.Fatal("invalid primary replaced", err)
	}
	if _, err := os.Stat(filepath.Join(legacy, jwtKeyFilename)); err != nil {
		t.Fatal("legacy changed", err)
	}
}

func TestAuthStateCanonicalLegacyDuplicates(t *testing.T) {
	root := t.TempDir()
	primary, legacy := filepath.Join(root, "auth"), filepath.Join(root, "legacy")
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	relative, err := filepath.Rel(root, legacy)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalAuthPath(relative)
	if err != nil || canonical != legacy {
		t.Fatal("relative alias canonicalization", err)
	}
	key, err := loadOrCreateJWTKey(primary, legacy, relative, legacy)
	if err != nil || len(key) < 32 {
		t.Fatal("duplicate missing legacy paths", err)
	}
	t.Run("symlink", func(t *testing.T) {
		alias := filepath.Join(root, "alias")
		if err := os.Symlink(legacy, alias); err != nil {
			t.Skip(err)
		}
		canonicalAlias, err := canonicalAuthPath(alias)
		if err != nil || canonicalAlias != legacy {
			t.Fatal("symlink alias canonicalization", err)
		}
		other := filepath.Join(root, "other-auth")
		if _, err := loadOrCreateJWTKey(other, legacy, alias, relative); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAuthStateInvalidLegacyFailsClosed(t *testing.T) {
	root := t.TempDir()
	primary, base, data := filepath.Join(root, "auth"), filepath.Join(root, "base"), filepath.Join(root, "data")
	for _, dir := range []string{base, data} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, jwtKeyFilename), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, jwtKeyFilename), bytes.Repeat([]byte("d"), 32), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateJWTKey(primary, data, base); err == nil {
		t.Fatal("invalid first legacy accepted")
	}
	if _, err := os.Stat(filepath.Join(primary, jwtKeyFilename)); !os.IsNotExist(err) {
		t.Fatal("destination published despite invalid source")
	}
}

func TestAuthStateMigrationCleanupRetried(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permissions required")
	}
	root := t.TempDir()
	primary, legacy := filepath.Join(root, "auth"), filepath.Join(root, "legacy")
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(legacy, jwtKeyFilename)
	key := bytes.Repeat([]byte("k"), 32)
	if err := os.WriteFile(path, key, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(legacy, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(legacy, 0700) })
	_, first := loadOrCreateJWTKey(primary, legacy)
	if first == nil {
		t.Skip("filesystem permits removal from read-only directory")
	}
	if _, err := os.Stat(filepath.Join(primary, jwtKeyFilename)); err != nil {
		t.Fatal("publication did not precede cleanup failure", err)
	}
	if _, err := loadOrCreateJWTKey(primary, legacy); err == nil {
		t.Fatal("retry ignored matching legacy cleanup failure")
	}
	if err := os.Chmod(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOrCreateJWTKey(primary, legacy)
	if err != nil || !bytes.Equal(loaded, key) {
		t.Fatal("cleanup recovery changed key", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("matching legacy remains", err)
	}
}

func TestAuthStatePrimaryFileAliasesPreserved(t *testing.T) {
	for _, kind := range []string{"case", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			primary := filepath.Join(root, "AuthState")
			if err := os.Mkdir(primary, 0700); err != nil {
				t.Fatal(err)
			}
			key, err := loadOrCreateJWTKey(primary, filepath.Join(root, "unused"))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(primary, jwtKeyFilename)
			var alias string
			if kind == "case" {
				alias = filepath.Join(root, "authstate")
			} else {
				alias = filepath.Join(root, "linked")
				if err := os.Mkdir(alias, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(path, filepath.Join(alias, jwtKeyFilename)); err != nil {
					t.Skip(err)
				}
			}
			primaryInfo, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			aliasInfo, err := os.Stat(filepath.Join(alias, jwtKeyFilename))
			if kind == "case" && (os.IsNotExist(err) || (err == nil && !os.SameFile(primaryInfo, aliasInfo))) {
				t.Skip("filesystem has case-sensitive names")
			}
			if err != nil || !os.SameFile(primaryInfo, aliasInfo) {
				t.Fatal("fixture does not alias primary", err)
			}
			for range 2 {
				loaded, err := loadOrCreateJWTKey(primary, alias, alias)
				if err != nil || !bytes.Equal(loaded, key) {
					t.Fatal("alias cleanup changed primary", err)
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatal("primary removed", err)
				}
				if _, err := os.Stat(filepath.Join(alias, jwtKeyFilename)); err != nil {
					t.Fatal("same-file alias removed", err)
				}
			}
		})
	}
}
