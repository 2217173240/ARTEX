package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/distribution"
	"github.com/Autumn-27/artex/selfupdate"
)

func TestUpdateHandlersRejectInvalidProxyBeforeCacheAndTask(t *testing.T) {
	originalChannel, originalCache, originalHub := distribution.BuildChannel, relCache, updHub
	t.Cleanup(func() {
		distribution.BuildChannel, relCache, updHub = originalChannel, originalCache, originalHub
	})
	distribution.BuildChannel = "portable"
	s := &Server{m: &Manager{globalProxy: "http://fixture-user:fixture-secret@127.0.0.1:%zz"}}
	for _, tc := range []struct {
		name, method, path string
		apply, warmCache   bool
		status             int
	}{
		{"check cold cache", http.MethodGet, "/api/update/check", false, false, http.StatusOK},
		{"check warm cache", http.MethodGet, "/api/update/check", false, true, http.StatusOK},
		{"check force", http.MethodGet, "/api/update/check?force=1", false, true, http.StatusOK},
		{"apply cold cache", http.MethodPost, "/api/update/apply", true, false, http.StatusBadRequest},
		{"apply warm cache", http.MethodPost, "/api/update/apply", true, true, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			relCache = newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
				calls++
				return &selfupdate.Release{TagName: "v99.0.0"}, nil
			})
			if tc.warmCache {
				relCache.rel, relCache.at = &selfupdate.Release{TagName: "v99.0.0"}, time.Now()
			}
			beforeRelease, beforeAt := relCache.rel, relCache.at
			initial := updateProgress{Phase: selfupdate.PhaseIdle, Percent: -1}
			updHub = &updateHub{cur: initial, subs: map[chan updateProgress]struct{}{}}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.apply {
				s.updateApply(w, r)
			} else {
				s.updateCheck(w, r)
			}
			if w.Code != tc.status {
				t.Fatalf("HTTP status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			var out map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out["error"] == nil || out["error"] == "" || out["latest"] != nil || out["ok"] != nil {
				t.Fatalf("invalid proxy response: %v", out)
			}
			if strings.Contains(w.Body.String(), "fixture-user") || strings.Contains(w.Body.String(), "fixture-secret") {
				t.Fatal("HTTP response exposed proxy credentials")
			}
			if calls != 0 || relCache.rel != beforeRelease || relCache.at != beforeAt || relCache.err != nil {
				t.Fatal("invalid proxy reached release fetch or mutated cache")
			}
			if progress, running := updHub.snapshot(); running || progress != initial {
				t.Fatalf("invalid proxy changed update task lifecycle: %+v running=%t", progress, running)
			}
		})
	}
}
