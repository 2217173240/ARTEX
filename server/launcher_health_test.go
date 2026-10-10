package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLauncherHealthCorrelation(t *testing.T) {
	for _, token := range []string{"", "bad\r\nvalue", strings.Repeat("g", 64), strings.Repeat("a", 64)} {
		t.Run(token, func(t *testing.T) {
			t.Setenv("ARTEX_LAUNCH_TOKEN", token)
			w := httptest.NewRecorder()
			(&Server{}).health(w, httptest.NewRequest("GET", "/api/health", nil))
			want := ""
			if token == strings.Repeat("a", 64) {
				want = token
			}
			if got := w.Header().Get("X-ARTEX-Launch-Token"); got != want {
				t.Fatalf("correlation=%q want=%q", got, want)
			}
			if w.Code != 200 {
				t.Fatal(w.Code)
			}
		})
	}
}
