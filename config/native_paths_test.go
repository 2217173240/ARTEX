package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/distribution"
)

func TestApplicationHomeDoesNotReadWorkingDirectoryConfig(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Chdir(cwd)
	t.Setenv("ARTEX_HOME", home)
	t.Setenv("ARTEX_CONFIG", "")
	if err := os.WriteFile("config.json", []byte(`{"database":{"dsn":"wrong-directory"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := BaseDir(); got != home {
		t.Fatal(got)
	}
	if got := Path(); got != filepath.Join(home, "config.json") {
		t.Fatal(got)
	}
	if got := Load().Database.DSN; got != "" {
		t.Fatalf("unexpected configuration %q", got)
	}
}

func TestManagedBuildUsesUserConfiguration(t *testing.T) {
	old := distribution.BuildChannel
	distribution.BuildChannel = "pkg"
	defer func() { distribution.BuildChannel = old }()
	t.Setenv("ARTEX_HOME", "")
	t.Setenv("ARTEX_CONFIG", "")
	home, err := distribution.UserHome()
	if err != nil {
		t.Fatal(err)
	}
	if got := BaseDir(); got != home {
		t.Fatal(got)
	}
	if got := Path(); got != filepath.Join(home, "config.json") {
		t.Fatal(got)
	}
}
