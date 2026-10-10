package distribution

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBuildChannelPolicy(t *testing.T) {
	original := BuildChannel
	t.Cleanup(func() { BuildChannel = original })
	t.Setenv("ARTEX_BUILD_CHANNEL", "portable")
	for _, channel := range []string{"portable", "msi", "pkg", "deb", "rpm", "", "typo"} {
		BuildChannel = channel
		want := channel != "portable"
		if Managed() != want {
			t.Fatalf("%q managed = %v", channel, Managed())
		}
		if (channel == "" || channel == "typo") && Channel() != "unknown" {
			t.Fatal("unknown channel must fail closed")
		}
	}
}

func TestUserHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux XDG contract")
	}
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	got, err := UserHome()
	if err != nil || got != filepath.Join(root, "artex") {
		t.Fatalf("%q, %v", got, err)
	}
	t.Setenv("XDG_DATA_HOME", "relative")
	home, _ := os.UserHomeDir()
	got, err = UserHome()
	if err != nil || got != filepath.Join(home, ".local", "share", "artex") {
		t.Fatalf("%q, %v", got, err)
	}
}
