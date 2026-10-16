package server

import "testing"

func TestParseTargetRejectsInvalidAddressesAndPorts(t *testing.T) {
	for _, input := range []string{
		"999.999.999.999", "300.1.2.3", "1.2.3.4.5",
		"audit the app upgraded to 1.2.3.4444", "2.4.1.300",
		"9999.1.2.3", "999.1.2.3", "192.0.2.1:99999", "192.0.2.1:123456",
		"192.0.2.1:65535x", "192.0.2.1:80example.com", "192.0.2.1:abc.com", "999.999.999.999:80example.com",
		"example.com:8080x", "example.com:abc",
		"192.0.2.1:0", "192.0.2.1:-1", "192.0.2.1:abc",
		"example.com:65536", "example.com:123456", "example.com:0",
		"http://999.999.999.999/", "http://300.1.2.3/", "http://1.2.3.4.5/",
		"http://192.0.2.1:99999/", "https://example.com:65536/path",
		"https://example.com:0/path", "https://example.com:/path",
		"https://example.com:abc/path", "https://example.com:65536.",
		"https://example.com:abc)",
	} {
		t.Run(input, func(t *testing.T) {
			if scheme, host, port, ok := parseTarget(input); ok {
				t.Fatalf("parseTarget(%q) = (%q, %q, %d, true), want rejection", input, scheme, host, port)
			}
		})
	}
}

func TestParseTargetPreservesSupportedTargets(t *testing.T) {
	for _, tc := range []struct {
		input, scheme, host string
		port                int
	}{
		{"scan 192.0.2.1 please", "http", "192.0.2.1", 80},
		{"192.0.2.1:65535", "http", "192.0.2.1", 65535},
		{"192.0.2.1:1", "http", "192.0.2.1", 1},
		{"scan 192.0.2.1. Then report.", "http", "192.0.2.1", 80},
		{"192.0.2.1:8443", "https", "192.0.2.1", 8443},
		{"audit Example.COM", "https", "example.com", 443},
		{"example.com:8080", "http", "example.com", 8080},
		{"example.com:65535", "http", "example.com", 65535},
		{"scan example.com. Then report.", "https", "example.com", 443},
		{"https://Example.COM/path?q=1", "https", "example.com", 443},
		{"http://localhost:8080/path", "http", "localhost", 8080},
		{"scan https://example.com:8080.", "https", "example.com", 8080},
		{"scan http://192.0.2.1:8080.", "http", "192.0.2.1", 8080},
		{"scan https://example.com:8080, please", "https", "example.com", 8080},
		{"scan https://example.com:8080) please", "https", "example.com", 8080},
		{"scan https://example.com:8080。", "https", "example.com", 8080},
		{"scan http://[2001:db8::1]) please", "http", "2001:db8::1", 80},
		{"http://192.0.2.1:65535/path", "http", "192.0.2.1", 65535},
		{"https://[2001:db8::1]/", "https", "2001:db8::1", 443},
		{"http://[2001:db8::1]:8080/", "http", "2001:db8::1", 8080},
		{"http://[fe80::1%25eth0]:8080/", "http", "fe80::1%eth0", 8080},
		// Preserve the existing seed extraction from a CIDR scope description.
		{"192.0.2.0/24", "http", "192.0.2.0", 80},
	} {
		t.Run(tc.input, func(t *testing.T) {
			scheme, host, port, ok := parseTarget(tc.input)
			if !ok || scheme != tc.scheme || host != tc.host || port != tc.port {
				t.Fatalf("parseTarget(%q) = (%q, %q, %d, %t), want (%q, %q, %d, true)", tc.input, scheme, host, port, ok, tc.scheme, tc.host, tc.port)
			}
		})
	}
}
