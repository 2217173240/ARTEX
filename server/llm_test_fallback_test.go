package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestLLMTestStoredKeyFallbackNormalization(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("explicit ARTEX_PG_DSN fixture required")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	const storedKey = "\n fixture-stored-key-only \t\n"
	for _, tc := range []struct {
		name, formKey, wantKey string
	}{
		{"stored fallback", "", "fixture-stored-key-only"},
		{"form takes precedence", "\n fixture-form-key-only \t\n", "fixture-form-key-only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotKey, gotSession atomic.Value
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotKey.Store(r.Header.Get("x-api-key"))
				gotSession.Store(r.Header.Get("x-fixture-session"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"fixture-message","type":"message","role":"assistant","model":"fixture-model","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			t.Cleanup(fake.Close)
			id, err := d.SaveProfile(&db.LLMProfile{Name: "test-normalization-fixture", Format: "anthropic", Model: "fixture-model", BaseURL: fake.URL, APIKey: storedKey, SessionHeaderKey: "x-fixture-session"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
			stored, err := d.ProfileByID(id)
			if err != nil || stored == nil || stored.APIKey != storedKey {
				t.Fatalf("raw stored-key fixture changed: %+v %v", stored, err)
			}
			// The UI sends endpoint/model fields while leaving the stored key
			// hidden. Runtime construction already normalizes this same key.
			runtime := agent.ConfigFrom(stored.Format, stored.Model, stored.BaseURL, stored.APIKey, stored.Proxy)
			if runtime.APIKey != strings.TrimSpace(storedKey) {
				t.Fatal("runtime normalization changed")
			}
			body, err := json.Marshal(map[string]any{"provider": "anthropic", "model": "fixture-model", "base_url": fake.URL, "profile_id": id, "api_key": tc.formKey, "streaming": false})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			(&Server{m: &Manager{pg: d}}).testLLM(rec, httptest.NewRequest(http.MethodPost, "/api/llm/test", bytes.NewReader(body)))
			var response struct {
				OK    bool   `json:"ok"`
				Reply string `json:"reply"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusOK || !response.OK || response.Reply != "OK" {
				t.Fatalf("test request failed: status=%d reply=%+v", rec.Code, response)
			}
			if got, _ := gotKey.Load().(string); got != tc.wantKey {
				t.Fatalf("fake provider received key %q want %q", got, tc.wantKey)
			}
			if got, _ := gotSession.Load().(string); !strings.HasPrefix(got, "conntest-") {
				t.Fatalf("stored session header fallback changed: %q", got)
			}
			stored, err = d.ProfileByID(id)
			if err != nil || stored.APIKey != storedKey {
				t.Fatal("connection test modified stored profile")
			}
		})
	}
}
