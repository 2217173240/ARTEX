// Package distribution describes how this executable was installed.
package distribution

import (
	"os"
	"path/filepath"
	"runtime"
)

// BuildChannel is set by the release linker. Environment variables cannot alter it.
var BuildChannel = "portable"

// Channel returns unknown for unrecognized builds, which remain managed.
func Channel() string {
	switch BuildChannel {
	case "portable", "msi", "pkg", "deb", "rpm":
		return BuildChannel
	default:
		return "unknown"
	}
}

// Managed reports whether updates must use the installer or package manager.
func Managed() bool { return Channel() != "portable" }

// UserHome returns the per-user application data root, separate from installed files.
func UserHome() (string, error) {
	switch runtime.GOOS {
	case "windows":
		root, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(root, "ARTEX"), nil
	case "darwin":
		root, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(root, "ARTEX"), nil
	default:
		if root := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(root) {
			return filepath.Join(root, "artex"), nil
		}
		root, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(root, ".local", "share", "artex"), nil
	}
}
