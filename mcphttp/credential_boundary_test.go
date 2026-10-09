package mcphttp

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSSEEndpointOrigin(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		valid    bool
	}{
		{"/message?session=1", true}, {"https://example.com:443/message", true},
		{"http://example.com/message", false}, {"//other.example/message", false},
		{"https://example.com:444/message", false},
	} {
		_, err := readSSEEndpoint(bufio.NewReader(strings.NewReader("data: "+tc.endpoint+"\n")), "https://example.com/sse")
		if (err == nil) != tc.valid {
			t.Errorf("%s: %v", tc.endpoint, err)
		}
	}
}

func TestMCPRedirectCredentialBoundary(t *testing.T) {
	var received atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer sink.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	for _, legacy := range []bool{false, true} {
		var err error
		if legacy {
			_, err = NewSSE(context.Background(), "test", source.URL, map[string]string{"X-Secret": "secret"}, false)
		} else {
			_, err = New(context.Background(), "test", source.URL, map[string]string{"X-Secret": "secret"}, false)
		}
		if err == nil {
			t.Errorf("legacy=%v: redirect accepted", legacy)
		}
	}
	if received.Load() != 0 {
		t.Fatal("redirect target received request")
	}
}

func TestSSEAnnouncedEndpointCredentialBoundary(t *testing.T) {
	var received atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer sink.Close()
	for _, endpoint := range []string{sink.URL + "/message", strings.TrimPrefix(sink.URL, "http:") + "/message"} {
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: endpoint\ndata: " + endpoint + "\n\n"))
		}))
		_, err := NewSSE(context.Background(), "test", source.URL+"/sse", map[string]string{"X-Secret": "secret"}, false)
		source.Close()
		if err == nil {
			t.Errorf("endpoint %s accepted", endpoint)
		}
	}
	if received.Load() != 0 {
		t.Fatal("announced endpoint received request")
	}
}
