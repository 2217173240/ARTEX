package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolatedConfig(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, "config.json")
	t.Setenv("ARTEX_HOME", home)
	t.Setenv("ARTEX_CONFIG", path)
	t.Setenv("ARTEX_PG_DSN", "")
	t.Setenv("ARTEX_SKILL_DIR", "")
	return home, path
}

func writeConfig(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadOptionalFiles(t *testing.T) {
	_, _ = isolatedConfig(t)
	if c, err := Load(); err != nil || c != (Config{}) {
		t.Fatalf("missing: %+v %v", c, err)
	}
	if _, _, err := PostgresDSN(); err == nil || !strings.Contains(err.Error(), "未找到数据库配置") {
		t.Fatalf("missing database: %v", err)
	}
	t.Setenv("ARTEX_CONFIG", os.DevNull)
	if c, err := Load(); err != nil || c != (Config{}) {
		t.Fatalf("null device: %+v %v", c, err)
	}
}

func TestLoadRejectsInvalidWithoutPartialValues(t *testing.T) {
	for _, tc := range []struct {
		name, contents string
		typeError      bool
	}{
		{"empty", "", false},
		{"syntax", `{"database":{"password":"secret-value"},`, false},
		{"type", `{"database":{"dsn":"secret-dsn","port":"bad"},"skill_dir":"secret-path"}`, true},
		{"numeric-password", `{"database":{"dsn":"secret-dsn","password":9876543210987654321}}`, true},
		{"trailing", `{"database":{"dsn":"secret-dsn"}} {}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, path := isolatedConfig(t)
			writeConfig(t, path, tc.contents)
			c, err := Load()
			if err == nil || c != (Config{}) {
				t.Fatalf("expected zero config and error: %+v %v", c, err)
			}
			var loadErr *LoadError
			if !errors.As(err, &loadErr) || loadErr.Path != path {
				t.Fatalf("missing path/type: %v", err)
			}
			if tc.typeError {
				var target *json.UnmarshalTypeError
				if !errors.As(err, &target) {
					t.Fatalf("missing JSON type error: %v", err)
				}
			} else {
				var target *json.SyntaxError
				if !errors.As(err, &target) {
					t.Fatalf("missing JSON syntax error: %v", err)
				}
			}
			for _, secret := range []string{"secret-value", "secret-dsn", "secret-path", "9876543210987654321", "bad"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("diagnostic exposed config value: %v", err)
				}
			}
			if dsn, source, err := PostgresDSN(); err == nil || dsn != "" || source != "" {
				t.Fatalf("partial database escaped: %q %q %v", dsn, source, err)
			}
		})
	}
}

func TestLoadUnknownFieldsRemainCompatible(t *testing.T) {
	_, path := isolatedConfig(t)
	writeConfig(t, path, `{"database":{"dsn":"postgres://fixture/db","future":true},"future":{}}`)
	if c, err := Load(); err != nil || c.Database.DSN != "postgres://fixture/db" {
		t.Fatalf("unknown fields: %+v %v", c, err)
	}
}

func TestLoadReadFailures(t *testing.T) {
	home, path := isolatedConfig(t)
	t.Setenv("ARTEX_CONFIG", home)
	_, err := Load()
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("directory should retain filesystem error: %v", err)
	}
	if !errors.Is(err, pathErr.Err) {
		t.Fatalf("filesystem cause lost: %v", err)
	}
	t.Setenv("ARTEX_CONFIG", path)
	writeConfig(t, path, `{}`)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0600) })
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("current account can read mode-000 file")
	}
	if _, err := Load(); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("permission error: %v", err)
	}
}

func TestEnvironmentBypassesInvalidFile(t *testing.T) {
	home, path := isolatedConfig(t)
	writeConfig(t, path, `invalid`)
	t.Setenv("ARTEX_PG_DSN", " postgres://environment/db ")
	if dsn, _, err := PostgresDSN(); err != nil || dsn != "postgres://environment/db" {
		t.Fatalf("environment database: %q %v", dsn, err)
	}
	dir := filepath.Join(home, "environment-skills")
	t.Setenv("ARTEX_SKILL_DIR", dir)
	if got, err := SkillDir(); err != nil || got != dir {
		t.Fatalf("environment skills: %q %v", got, err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("skills not created: %v", err)
	}
}

func TestSkillDirDegradesWithSafePath(t *testing.T) {
	home, path := isolatedConfig(t)
	custom := filepath.Join(home, "partial-custom")
	contents, err := json.Marshal(map[string]any{"skill_dir": custom, "database": map[string]any{"port": "bad"}})
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, path, string(contents))
	want := filepath.Join(home, "skills")
	got, err := SkillDir()
	var loadErr *LoadError
	if got != want || !errors.As(err, &loadErr) {
		t.Fatalf("unsafe degradation: %q %v", got, err)
	}
	if _, err := os.Stat(custom); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial custom directory used: %v", err)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("default not created: %v", err)
	}
}

func TestSkillDirMkdirFailurePreservesPathAndFile(t *testing.T) {
	home, _ := isolatedConfig(t)
	file := filepath.Join(home, "existing-file")
	writeConfig(t, file, "preserve")
	t.Setenv("ARTEX_SKILL_DIR", file)
	got, err := SkillDir()
	var pathErr *os.PathError
	if got != file || !errors.As(err, &pathErr) {
		t.Fatalf("mkdir error/path: %q %v", got, err)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "preserve" {
		t.Fatalf("existing file changed: %q %v", b, err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("existing mode changed: %v", err)
	}
}

func TestSkillDirPreservesExistingDirectoryMode(t *testing.T) {
	home, _ := isolatedConfig(t)
	dir := filepath.Join(home, "private-skills")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_SKILL_DIR", dir)
	if got, err := SkillDir(); err != nil || got != dir {
		t.Fatalf("existing directory: %q %v", got, err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("existing directory mode changed: %v", err)
	}
}
