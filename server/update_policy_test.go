package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/distribution"
	"github.com/Autumn-27/artex/selfupdate"
)

func TestManagedUpdateHandlersRejectBeforeDependencies(t *testing.T) {
	original := distribution.BuildChannel
	t.Cleanup(func() { distribution.BuildChannel = original })
	for _, channel := range []string{"msi", "pkg", "deb", "rpm", "unknown"} {
		distribution.BuildChannel = channel
		s := &Server{}
		for _, handler := range []http.HandlerFunc{s.updateApply, s.updateRollback} {
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest(http.MethodPost, "/api/update/apply", nil))
			if w.Code != http.StatusConflict {
				t.Fatalf("%s: %d %s", channel, w.Code, w.Body.String())
			}
		}
	}
}

func TestManagedUpdateCheckStillChecksRelease(t *testing.T) {
	original, cache := distribution.BuildChannel, relCache
	t.Cleanup(func() { distribution.BuildChannel = original; relCache = cache })
	distribution.BuildChannel = "deb"
	relCache = newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		return &selfupdate.Release{TagName: "v99.0.0", HTMLURL: "https://evil.test"}, nil
	})
	s := &Server{m: &Manager{}}
	w := httptest.NewRecorder()
	s.updateCheck(w, httptest.NewRequest(http.MethodGet, "/api/update/check", nil))
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["mode"] != "managed" || out["distribution_channel"] != "deb" || out["has_backup"] != false || out["latest"] != "v99.0.0" {
		t.Fatalf("%v", out)
	}
	if out["release_url"] != "https://github.com/"+selfupdate.Repo+"/releases/tag/v99.0.0" || out["upgrade_instructions"] == "" {
		t.Fatalf("%v", out)
	}
}
