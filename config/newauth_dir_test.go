package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuthDirResolution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ARTEX_HOME", home)
	for _, tc := range []struct {
		name, override, want string
	}{
		{"default", "", home},
		{"whitespace", " \t\n", home},
		{"absolute", " \t" + filepath.Join(home, "private-auth") + "\n", filepath.Join(home, "private-auth")},
		{"relative", " auth-state ", "auth-state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARTEX_AUTH_DIR", tc.override)
			if got := AuthDir(); got != tc.want {
				t.Fatalf("AuthDir() = %q, want %q", got, tc.want)
			}
			if got := BaseDir(); got != home {
				t.Fatalf("BaseDir() = %q, want %q", got, home)
			}
		})
	}
}

func TestAuthDirDoesNotCreateDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ARTEX_HOME", home)
	dir := filepath.Join(home, "private-auth")
	t.Setenv("ARTEX_AUTH_DIR", dir)
	if got := AuthDir(); got != dir {
		t.Fatalf("AuthDir() = %q, want %q", got, dir)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("authentication directory must remain absent: %v", err)
	}
}
