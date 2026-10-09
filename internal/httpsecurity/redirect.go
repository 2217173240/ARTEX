// Package httpsecurity keeps configured HTTP credentials within their origin.
package httpsecurity

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SameOrigin compares scheme, hostname, and effective port. Paths may differ.
func SameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// SameOriginRedirect rejects redirects before configured headers can leave the
// initial origin, while retaining Go's default limit of ten redirects.
func SameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if len(via) == 0 || !SameOrigin(via[0].URL, req.URL) {
		return fmt.Errorf("cross-origin HTTP redirect rejected")
	}
	return nil
}
