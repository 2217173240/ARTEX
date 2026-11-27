package traffic

import (
	"strings"
	"testing"
)

func TestValidateProxyURLScrubsCredentials(t *testing.T) {
	const user = "alice"
	const pass = "supersecret"
	const creds = user + ":" + pass

	cases := []struct {
		name string
		raw  string
	}{
		{"invalid URL", "http://" + creds + "@127.0.0.1:%zz"},
		{"invalid URL bad port", "https://" + creds + "@proxy.example:badport"},
		{"missing scheme with //", "//" + creds + "@10.0.0.1:8080"},
		{"missing scheme bare", creds + "@10.0.0.1:8080"},
		{"unsupported scheme", "ftp://" + creds + "@10.0.0.1:21"},
		{"missing host", "http://" + creds + "@"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := ValidateProxyURL(tc.raw)
			if err == nil {
				t.Fatalf("ValidateProxyURL(%q) = %v, want error", tc.raw, u)
			}
			msg := err.Error()
			for _, secret := range []string{creds, user, pass} {
				if strings.Contains(msg, secret) {
					t.Errorf("error %q leaked credential substring %q", msg, secret)
				}
			}
		})
	}
}
