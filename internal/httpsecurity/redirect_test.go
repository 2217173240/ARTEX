package httpsecurity

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestSameOrigin(t *testing.T) {
	base, _ := url.Parse("https://EXAMPLE.com/path")
	for _, tc := range []struct {
		target string
		want   bool
	}{
		{"https://example.com:443/other", true},
		{"http://example.com/other", false},
		{"https://example.com:444/other", false},
		{"https://sub.example.com/other", false},
	} {
		u, _ := url.Parse(tc.target)
		if got := SameOrigin(base, u); got != tc.want {
			t.Errorf("%s: got %v", tc.target, got)
		}
	}
}

func TestRedirectKeepsHeadersWithinOrigin(t *testing.T) {
	var escaped atomic.Bool
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped.Store(true) }))
	defer sink.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/next", http.StatusFound)
			return
		}
		if r.Header.Get("X-Secret") != "secret" {
			t.Error("same-origin header missing")
		}
		http.Redirect(w, r, sink.URL, http.StatusFound)
	}))
	defer source.Close()
	req, _ := http.NewRequest(http.MethodGet, source.URL+"/start", nil)
	req.Header.Set("X-Secret", "secret")
	_, err := (&http.Client{CheckRedirect: SameOriginRedirect}).Do(req)
	if err == nil || escaped.Load() {
		t.Fatalf("err=%v escaped=%v", err, escaped.Load())
	}
}

func TestRedirectLimit(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
	via := make([]*http.Request, 10)
	for i := range via {
		via[i] = req
	}
	if SameOriginRedirect(req, via) == nil {
		t.Fatal("redirect limit missing")
	}
}
