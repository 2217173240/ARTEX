package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/distribution"
)

func TestManagedBuildRefusesReplacement(t *testing.T) {
	original := distribution.BuildChannel
	t.Cleanup(func() { distribution.BuildChannel = original })
	for _, channel := range []string{"msi", "pkg", "deb", "rpm", "unknown", ""} {
		distribution.BuildChannel = channel
		// Refusal must happen before dereferencing a release/client or touching paths.
		if err := Stage(t.Context(), nil, nil, "0.3.17", nil); !errors.Is(err, ErrManagedInstallation) {
			t.Fatalf("%s Stage: %v", channel, err)
		}
		if err := Rollback(); !errors.Is(err, ErrManagedInstallation) {
			t.Fatalf("%s Rollback: %v", channel, err)
		}
		action, state := Bootstrap()
		if action != Continue || state != (State{}) || HasBackup() {
			t.Fatalf("managed Bootstrap changed state: %v %+v", action, state)
		}
		dir := t.TempDir()
		current := filepath.Join(dir, "artex")
		if err := os.WriteFile(current, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
		paths := Paths{Current: current, Old: current + ".old", New: current + ".new"}
		if !errors.Is(swap(paths), ErrManagedInstallation) || !errors.Is(rollback(paths), ErrManagedInstallation) {
			t.Fatal("lower-level replacement allowed")
		}
		data, _ := os.ReadFile(current)
		if string(data) != "original" {
			t.Fatal("current binary changed")
		}
	}
}

func TestReleaseURLUsesOwnRepository(t *testing.T) {
	if got := ReleaseURL("v0.3.18"); got != "https://github.com/"+Repo+"/releases/tag/v0.3.18" {
		t.Fatal(got)
	}
	for _, tag := range []string{"https://evil.test", "v0.3.18/../../evil", "dev", ""} {
		if got := ReleaseURL(tag); got != "" {
			t.Fatalf("invalid tag %q accepted: %s", tag, got)
		}
	}
}
