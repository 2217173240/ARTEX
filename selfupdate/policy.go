package selfupdate

import (
	"errors"
	"net/url"

	"github.com/Autumn-27/artex/distribution"
)

var ErrManagedInstallation = errors.New("installer-managed ARTEX must be upgraded using its installer or package manager")

// UpgradeInstructions explains the installation channel's upgrade route.
func UpgradeInstructions() string {
	switch distribution.Channel() {
	case "msi":
		return "Download the new Windows MSI from this release, stop ARTEX, and run the installer to upgrade."
	case "pkg":
		return "Download the new macOS PKG from this release, stop ARTEX, and run the installer to upgrade."
	case "deb":
		return "Download the matching DEB from this release, stop ARTEX, and install it with sudo apt install ./<package>.deb."
	case "rpm":
		return "Download the matching RPM from this release, stop ARTEX, and install it with sudo dnf install ./<package>.rpm."
	default:
		return "Upgrade ARTEX through the installer or package manager used to install it."
	}
}

// ReleaseURL constructs a link only to this application's own verified release tag.
func ReleaseURL(tag string) string {
	if _, ok := parseVersion(tag); !ok {
		return ""
	}
	return "https://github.com/" + Repo + "/releases/tag/" + url.PathEscape(tag)
}
