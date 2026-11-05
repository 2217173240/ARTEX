package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestStoredLLMEndpointIdentity(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "https://env.example/v1/")
	for _, tc := range []struct {
		name, formatA, baseA, formatB, baseB string
		want                                 bool
	}{
		{"full chat path", "openai", "https://EXAMPLE.com:443/v1/chat/completions/", "openai", "https://example.com/v1", true},
		{"bearer family", "openai", "https://example.com/v1", "openai-responses", "https://example.com/v1/responses", true},
		{"auth family", "openai", "https://example.com/v1", "anthropic", "https://example.com/v1", false},
		{"tenant path", "openai", "https://example.com/a", "openai", "https://example.com/b", false},
		{"query", "openai", "https://example.com/v1?a=1", "openai", "https://example.com/v1?a=2", false},
		{"userinfo", "openai", "https://alice@example.com/v1", "openai", "https://bob@example.com/v1", false},
		{"raw env trailing slash", "openai", "", "openai", "https://env.example/v1", false},
		{"invalid", "openai", "bad", "openai", "bad", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := agent.ConfigFrom(tc.formatA, "", tc.baseA, "", "")
			b := agent.ConfigFrom(tc.formatB, "", tc.baseB, "", "")
			if got := sameLLMKeyEndpoint(a, b); got != tc.want {
				t.Fatalf("match=%v want %v", got, tc.want)
			}
		})
	}
}

func TestResolveStoredLLMKey(t *testing.T) {
	target := agent.ConfigFrom("openai", "", "https://target.example/v1", "", "")
	profile := target
	profile.APIKey = " profile "
	global := target
	global.APIKey = " global "
	for _, tc := range []struct {
		name, explicit  string
		profile, global *agent.Config
		want            string
	}{
		{"explicit", " explicit ", &profile, &global, "explicit"},
		{"profile", "", &profile, &global, "profile"},
		{"global", "", nil, &global, "global"},
		{"missing", "", nil, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := target
			c.APIKey = tc.explicit
			got, err := resolveLLMKey(c, tc.profile, tc.global)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("key selection failed: err=%v", err)
			}
		})
	}
	profile.BaseURL = "https://other.example/v1"
	if got, err := resolveLLMKey(target, &profile, &global); err != nil || got != "global" {
		t.Fatal("matching global fallback failed")
	}
	if _, err := resolveLLMKey(target, &profile, nil); err == nil || !strings.Contains(err.Error(), "当前目标") {
		t.Fatal("mismatch must explain target")
	}
}

func canceledLLMRequest(t *testing.T, h http.HandlerFunc, body string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	h(w, r)
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Fatal("canceled request unexpectedly succeeded")
	}
	return result.Error
}

func TestLLMGlobalEndpointGuard(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "environment-key")
	s := &Server{llmCfg: agent.ConfigFrom("openai", "", "https://saved.example/v1", "stored-key", "")}
	errText := canceledLLMRequest(t, s.testLLM, `{"provider":"openai","base_url":"https://other.example/v1"}`)
	if !strings.Contains(errText, "当前目标端点与已存") {
		t.Fatalf("guard did not run before provider: %s", errText)
	}
	for _, body := range []string{
		`{"provider":"openai","base_url":"https://saved.example/v1"}`,
		`{"provider":"openai","base_url":"https://other.example/v1","api_key":" explicit "}`,
	} {
		if text := canceledLLMRequest(t, s.testLLM, body); !strings.Contains(text, "context canceled") {
			t.Fatalf("authorized target must reach canceled provider request: %s", text)
		}
	}
	errText = canceledLLMRequest(t, s.pgListModels, `{"provider":"openai","base_url":"https://saved.example/v1"}`)
	if !strings.Contains(errText, "未提供 API Key") {
		t.Fatalf("models must not use global or environment key: %s", errText)
	}
}

func TestLLMProfileEndpointGuardPostgres(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("ARTEX_PG_DSN required for isolated PostgreSQL integration")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close() })
	pid, err := pg.SaveProfile(&db.LLMProfile{Name: "r42-endpoint-binding", Format: "openai", Model: "model", BaseURL: "https://saved.example/v1", APIKey: "profile-key"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pg.Exec("DELETE FROM llm_profiles WHERE id=$1", pid); err != nil {
			t.Error(err)
		}
	})
	s := &Server{m: &Manager{pg: pg}}
	for _, h := range []http.HandlerFunc{s.testLLM, s.pgListModels} {
		body, err := json.Marshal(map[string]any{"provider": "openai", "base_url": "https://other.example/v1", "profile_id": pid})
		if err != nil {
			t.Fatal(err)
		}
		if text := canceledLLMRequest(t, h, string(body)); !strings.Contains(text, "当前目标端点与已存") {
			t.Fatalf("profile guard did not run: %s", text)
		}
		body, err = json.Marshal(map[string]any{"provider": "openai", "base_url": "https://saved.example/v1/chat/completions", "profile_id": pid})
		if err != nil {
			t.Fatal(err)
		}
		if text := canceledLLMRequest(t, h, string(body)); !strings.Contains(text, "context canceled") {
			t.Fatalf("matching stored profile rejected: %s", text)
		}
		body, err = json.Marshal(map[string]any{"provider": "openai", "base_url": "https://other.example/v1", "profile_id": pid, "api_key": " explicit "})
		if err != nil {
			t.Fatal(err)
		}
		if text := canceledLLMRequest(t, h, string(body)); !strings.Contains(text, "context canceled") {
			t.Fatalf("explicit key must reach canceled request: %s", text)
		}
	}
}
