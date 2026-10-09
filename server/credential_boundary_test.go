package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestConfiguredHTTPRedirectBoundary(t *testing.T) {
	var received atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
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
	raw, _ := json.Marshal(httpExec{URL: source.URL + "/start", Headers: map[string]string{"X-Secret": "secret"}})
	res, err := (&Server{}).runHTTPTool(context.Background(), raw, map[string]any{}, nil)
	if err != nil || !res.IsError || received.Load() != 0 {
		t.Fatalf("err=%v isError=%v received=%d", err, res.IsError, received.Load())
	}
}

func TestModelDiscoveryRedirectBoundary(t *testing.T) {
	var received atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer sink.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			http.Redirect(w, r, "/next", http.StatusFound)
			return
		}
		if r.Header.Get("X-Api-Key") != "secret" {
			t.Error("same-origin API key missing")
		}
		http.Redirect(w, r, sink.URL, http.StatusFound)
	}))
	defer source.Close()
	raw, _ := json.Marshal(map[string]string{"provider": "anthropic", "base_url": source.URL, "api_key": "secret"})
	w := httptest.NewRecorder()
	(&Server{}).pgListModels(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw))))
	if received.Load() != 0 || !strings.Contains(w.Body.String(), "cross-origin") {
		t.Fatalf("received=%d response=%s", received.Load(), w.Body.String())
	}
}
