package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
)

func TestWorkerSystemPromptSplitBoundary(t *testing.T) {
	intent := &db.Node{
		ID:      42,
		Kind:    db.KindIntent,
		Payload: json.RawMessage(`{"summary":"probe /login"}`),
	}
	runDir := filepath.Join("work", "tasks", "7", "i42")
	constraints := "\n\n【操作约束】：\n禁止的操作：\n- Do not touch /admin"
	assetsJSON := `[{"id":1,"domain":"example.com"}]`
	staticSys := workerStaticSystem("127.0.0.1:8080", "/ca.pem", "work")

	cases := []struct {
		name         string
		def          DeferredInfo
		wantBoundary int
	}{
		{
			name: "without global deferred tools",
			def: DeferredInfo{
				FindingGuidance: "\n\nFINDING_GUIDE",
				CaseGuidance:    "\n\nCASE_GUIDE",
			},
			wantBoundary: 1,
		},
		{
			name: "with global deferred tools",
			def: DeferredInfo{
				FindingGuidance: "\n\nFINDING_GUIDE",
				CaseGuidance:    "\n\nCASE_GUIDE",
				Deferred:        []string{"mcp__browser__navigate"},
				GlobalNames:     []string{"mcp__browser__navigate"},
			},
			wantBoundary: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sys, boundary := buildWorkerSystemPrompt(staticSys, constraints, tc.def, runDir, intent, assetsJSON)
			if boundary != tc.wantBoundary {
				t.Fatalf("boundary = %d, want %d", boundary, tc.wantBoundary)
			}
			if boundary <= 0 || boundary >= len(sys) {
				t.Fatalf("expected 0 < boundary < len(sys), got boundary=%d len=%d", boundary, len(sys))
			}

			prefix := strings.Join(sys[:boundary], "\n\n")
			suffix := strings.Join(sys[boundary:], "\n\n")

			for _, want := range []string{
				"执行者",
				"traffic_search",
				"不可信数据规则",
				"Do not touch /admin",
				"FINDING_GUIDE",
				"CASE_GUIDE",
			} {
				if !strings.Contains(prefix, want) {
					t.Errorf("static prefix missing %q", want)
				}
			}
			if len(tc.def.GlobalNames) > 0 && !strings.Contains(prefix, "<available-deferred-tools>") {
				t.Errorf("static prefix missing deferred tools block")
			}

			for _, forbidden := range []string{
				runDir,
				"probe /login",
				"意图 id: 42",
				"intent_assets",
				"开始执行上面这条意图",
			} {
				if strings.Contains(prefix, forbidden) {
					t.Errorf("static prefix must not contain per-intent content %q", forbidden)
				}
			}

			for _, want := range []string{
				"中间产物输出规约",
				runDir,
				"probe /login",
				"意图 id: 42",
				WrapUntrustedData("intent_assets", assetsJSON),
				"开始执行上面这条意图",
			} {
				if !strings.Contains(suffix, want) {
					t.Errorf("dynamic suffix missing %q", want)
				}
			}
		})
	}
}

func TestWorkerSessionAttachesAnthropicCacheControl(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		mu.Lock()
		bodies = append(bodies, m)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer srv.Close()

	prov, err := llm.NewProvider(llm.Config{
		Format:  llm.FormatAnthropic,
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-sonnet-4-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	workDir := t.TempDir()
	runDir := ensureRunDir(workDir, 7, 42)
	intent := &db.Node{
		ID:      42,
		Kind:    db.KindIntent,
		Payload: json.RawMessage(`{"summary":"probe /login"}`),
	}
	staticSys := workerStaticSystem("", "", workDir)
	system, boundary := buildWorkerSystemPrompt(staticSys, "", DeferredInfo{}, runDir, intent, "")

	s := agentcore.NewSession(agentcore.Options{
		Provider:        prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		PermissionMode:  permission.ModeBypass,
		WorkingDir:      runDir,
		MaxTurns:        1,
		NonStreaming:    true,
	})
	defer s.Close()

	if _, _, err := captureRunSession(context.Background(), s, "overview", nil); err != nil {
		t.Fatalf("captureRunSession failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) == 0 {
		t.Fatal("expected at least 1 Anthropic API request")
	}
	sysBlocks, ok := bodies[0]["system"].([]any)
	if !ok || len(sysBlocks) != 2 {
		t.Fatalf("expected 2 system blocks (cached prefix + dynamic per-intent suffix), got %#v", bodies[0]["system"])
	}
	b0, _ := sysBlocks[0].(map[string]any)
	b1, _ := sysBlocks[1].(map[string]any)
	cc, hasCC := b0["cache_control"].(map[string]any)
	if !hasCC || cc["type"] != "ephemeral" {
		t.Fatalf("static system block 0 missing cache_control=ephemeral: %#v", b0)
	}
	if _, hasCC1 := b1["cache_control"]; hasCC1 {
		t.Fatalf("dynamic system block 1 must not have cache_control: %#v", b1)
	}

	text0, _ := b0["text"].(string)
	text1, _ := b1["text"].(string)
	if !strings.Contains(text0, "不可信数据规则") || strings.Contains(text0, "probe /login") || strings.Contains(text0, runDir) {
		t.Fatalf("unexpected static block 0 text: %q", text0)
	}
	if !strings.Contains(text1, runDir) || !strings.Contains(text1, "probe /login") || !strings.Contains(text1, "意图 id: 42") {
		t.Fatalf("unexpected dynamic block 1 text: %q", text1)
	}
}
