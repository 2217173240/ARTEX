package agent

import (
	"net/http"
	"testing"
)

func TestQuotaAwareHTTPClientRedirectPolicy(t *testing.T) {
	client, err := quotaAwareHTTPClient("", " X-Fixture-Session ")
	if err != nil {
		t.Fatal(err)
	}
	if client.CheckRedirect == nil {
		t.Fatal("missing redirect policy")
	}
	initial, err := http.NewRequest(http.MethodPost, "https://provider.invalid/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	initial.Header.Set("X-Fixture-Session", "fixture-session")
	for _, tt := range []struct {
		name    string
		target  string
		count   int
		allowed bool
	}{
		{"same origin", "https://provider.invalid/other", 1, true},
		{"default port", "https://provider.invalid:443/other", 1, true},
		{"subdomain", "https://sub.provider.invalid/other", 1, false},
		{"different host", "https://other.invalid/other", 1, false},
		{"downgrade", "http://provider.invalid/other", 1, false},
		{"different scheme", "ftp://provider.invalid/other", 1, false},
		{"different port", "https://provider.invalid:8443/other", 1, false},
		{"nine requests", "https://provider.invalid/other", 9, true},
		{"ten request limit", "https://provider.invalid/other", 10, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, tt.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header = initial.Header.Clone()
			via := make([]*http.Request, tt.count)
			for i := range via {
				via[i] = initial
			}
			if err := client.CheckRedirect(req, via); (err == nil) != tt.allowed {
				t.Fatalf("redirect allowed = %v, want %v (error %v)", err == nil, tt.allowed, err)
			}
			if req.Header.Get("X-Fixture-Session") != "fixture-session" {
				t.Fatal("redirect policy changed session header")
			}
		})
	}
	wrapper, ok := client.Transport.(quotaAwareTransport)
	if !ok || wrapper.sessionHeaderKey != "X-Fixture-Session" {
		t.Fatal("client did not preserve configured session header")
	}
}
