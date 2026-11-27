package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/noa"
	"github.com/Autumn-27/norma/noaadapter"
	"github.com/Autumn-27/norma/permission"
)

func TestPlannerPromptParentIDsConstraint(t *testing.T) {
	if !strings.Contains(plannerDefaultTmpl, "fact 或 finding") {
		t.Fatalf("plannerDefaultTmpl must instruct parent_ids to use fact/finding IDs")
	}
	if !strings.Contains(plannerDefaultTmpl, "intent/goal/hint") {
		t.Fatalf("plannerDefaultTmpl must warn against passing intent/goal/hint IDs in parent_ids")
	}
	if strings.Contains(plannerDefaultTmpl, "派生自某上游意图") {
		t.Fatalf("plannerDefaultTmpl still contains stale instruction allowing intent parent_ids")
	}
}

func TestExpandDigestIsConcurrentReadOnly(t *testing.T) {
	ts := NewToolSet(nil, "planner")
	tl := ts.expandDigest()
	if !tl.IsReadOnly(nil) {
		t.Fatal("expand_digest.IsReadOnly(nil) = false, want true")
	}
	if !tl.IsConcurrencySafe(nil) {
		t.Fatal("expand_digest.IsConcurrencySafe(nil) = false, want true")
	}
}

func TestCompactionConfigClearsStaleARTEXTools(t *testing.T) {
	cfg := compactionConfig(1000)
	if cfg == nil {
		t.Fatal("compactionConfig returned nil")
	}
	cfg.MaxOutputTokens = 100

	payload := strings.Repeat("y", 400)
	toolNames := []string{
		"traffic_detail",
		"mcp__demo__scan",
		"expand_digest",
		"SearchExtraTools",
		"worker_trace_detail",
		"node_detail",
		"list_assets",
	}
	var msgs []llm.Message
	for i, name := range toolNames {
		id := fmt.Sprintf("call_%d", i)
		msgs = append(msgs,
			llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentBlock{{
					Type:  llm.BlockToolUse,
					ID:    id,
					Name:  name,
					Input: json.RawMessage(`{}`),
				}},
			},
			llm.Message{
				Role: llm.RoleUser,
				Content: []llm.ContentBlock{{
					Type:      llm.BlockToolResult,
					ToolUseID: id,
					Content:   []llm.ContentBlock{llm.TextBlock(fmt.Sprintf("%s-%s", name, payload))},
				}},
			},
		)
	}

	out := compaction.New(*cfg, nil).Pre(context.Background(), msgs, 0)
	for i, name := range toolNames {
		got := out[i*2+1].Content[0].Content[0].Text
		if i < 2 {
			if got != "[Old tool result content cleared]" {
				t.Fatalf("stale tool %d (%s) = %q, want cleared", i, name, got)
			}
		} else if got == "[Old tool result content cleared]" {
			t.Fatalf("recent tool %d (%s) was unexpectedly cleared", i, name)
		}
	}
}

func TestEnableNoaPropagatesContextWindow(t *testing.T) {
	// Build 10 messages (~25k tokens total): well above 75% of 32k (24k),
	// well below 45% of the 200k default (90k).
	body := strings.Repeat("a", 10000)
	var msgs []llm.Message
	for range 5 {
		msgs = append(msgs,
			llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(body)}},
			llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(body)}},
		)
	}

	optsSmall := agentcore.Options{
		SystemPrompt: []string{"static"},
		Compaction:   compactionConfig(32000),
	}
	enableNoa(&optsSmall, func() bool { return true }, t.TempDir(), "s-32k", 32000, nil)
	if optsSmall.Compaction != nil {
		t.Fatal("optsSmall.Compaction must be cleared when noa is enabled")
	}
	cSmall, ok := optsSmall.Compactor.(*noaadapter.Compactor)
	if !ok || cSmall == nil {
		t.Fatalf("optsSmall.Compactor = %T, want *noaadapter.Compactor", optsSmall.Compactor)
	}
	if optsSmall.DynamicBoundary != 2 {
		t.Fatalf("DynamicBoundary = %d, want 2", optsSmall.DynamicBoundary)
	}
	if viewSmall := cSmall.View(context.Background(), msgs); len(viewSmall) <= len(msgs) {
		t.Fatalf("32k window should trigger noa nudge: len(view)=%d, len(msgs)=%d", len(viewSmall), len(msgs))
	}

	optsLarge := agentcore.Options{
		SystemPrompt: []string{"static"},
		Compaction:   compactionConfig(200000),
	}
	enableNoa(&optsLarge, func() bool { return true }, t.TempDir(), "s-200k", 200000, nil)
	cLarge := optsLarge.Compactor.(*noaadapter.Compactor)
	if viewLarge := cLarge.View(context.Background(), msgs); len(viewLarge) != len(msgs) {
		t.Fatalf("200k window should not trigger noa nudge: len(view)=%d, want %d", len(viewLarge), len(msgs))
	}
}

func TestWorkerSessionWithNoaPreservesAnthropicCacheBoundary(t *testing.T) {
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
	runDir := filepath.Join(workDir, "tasks", "7", "i42")
	intent := &db.Node{
		ID:      42,
		Kind:    db.KindIntent,
		Payload: json.RawMessage(`{"summary":"probe /login"}`),
	}
	staticSys := workerStaticSystem("", "", workDir)
	system, boundary := buildWorkerSystemPrompt(staticSys, "", DeferredInfo{}, runDir, intent, "")

	opts := agentcore.Options{
		Provider:        prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		PermissionMode:  permission.ModeBypass,
		WorkingDir:      runDir,
		MaxTurns:        1,
		Compaction:      compactionConfig(128000),
		NonStreaming:    true,
	}
	enableNoa(&opts, func() bool { return true }, workDir, "t7-i42", 128000, nil)

	s := agentcore.NewSession(opts)
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
		t.Fatalf("expected 2 system blocks (cached prefix + dynamic suffix), got %#v", bodies[0]["system"])
	}
	b0, _ := sysBlocks[0].(map[string]any)
	b1, _ := sysBlocks[1].(map[string]any)
	cc, hasCC := b0["cache_control"].(map[string]any)
	if !hasCC || cc["type"] != "ephemeral" {
		t.Fatalf("static block 0 missing cache_control=ephemeral: %#v", b0)
	}
	if _, hasCC1 := b1["cache_control"]; hasCC1 {
		t.Fatalf("dynamic block 1 must not have cache_control: %#v", b1)
	}
	text0, _ := b0["text"].(string)
	text1, _ := b1["text"].(string)
	if !strings.Contains(text0, noa.SystemPromptHeader) {
		t.Fatalf("static block 0 missing noa prompt section: %q", text0)
	}
	if strings.Contains(text0, "probe /login") || strings.Contains(text0, runDir) {
		t.Fatalf("static block 0 leaked per-intent content: %q", text0)
	}
	if !strings.Contains(text1, runDir) || !strings.Contains(text1, "probe /login") || !strings.Contains(text1, "意图 id: 42") {
		t.Fatalf("dynamic block 1 missing per-intent content: %q", text1)
	}
}
