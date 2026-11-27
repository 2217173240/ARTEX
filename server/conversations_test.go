package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

func TestStartTriggeredRunNilGuards(t *testing.T) {
	t.Parallel()

	var nilSrv *Server
	nilSrv.StartTriggeredRun("a", "t", "m", 0, false, "", "")
	nilSrv.runTriggeredRun(triggeredRun{agentKey: "a", title: "t", message: "m"})

	noMgr := &Server{}
	noMgr.StartTriggeredRun("a", "t", "m", 0, false, "", "")
	noMgr.runTriggeredRun(triggeredRun{agentKey: "a", title: "t", message: "m"})

	noPG := &Server{m: &Manager{}}
	noPG.StartTriggeredRun("a", "t", "m", 0, false, "", "")
	noPG.runTriggeredRun(triggeredRun{agentKey: "a", title: "t", message: "m"})
}

func TestStartTriggeredRunProfileBoundWithoutGlobalChatAgent(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("explicit isolated ARTEX_PG_DSN required")
	}
	oldAugment, oldResolve, oldPrompt := agent.ToolAugment, agent.ToolResolve, agent.PromptOverride
	agent.ToolAugment = nil
	agent.ToolResolve = nil
	agent.PromptOverride = nil
	t.Cleanup(func() {
		agent.ToolAugment, agent.ToolResolve, agent.PromptOverride = oldAugment, oldResolve, oldPrompt
	})

	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		ctx:             ctx,
		m:               &Manager{dir: t.TempDir(), pg: pg, interceptor: intercept.New(pg)},
		chatBusy:        map[string]bool{},
		chatCancel:      map[string]context.CancelCauseFunc{},
		triggerQ:        map[string][]triggeredRun{},
		triggerActive:   map[string]int{},
		triggerCfg:      map[string]triggerBehavior{},
		profChatAgents:  map[int64]*agent.ChatAgent{},
		provByProfile:   map[int64]*provEntry{},
		requestLimiters: map[int64]*agent.RequestLimiter{},
	}

	var llmCalls atomic.Int32
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"fixture stop"}}`))
	}))
	defer mockLLM.Close()

	suffix := time.Now().UnixNano()
	boundKey := fmt.Sprintf("trig_bound_%d", suffix)
	unboundKey := fmt.Sprintf("trig_unbound_%d", suffix)

	profID, err := pg.SaveProfile(&db.LLMProfile{
		Name:    fmt.Sprintf("trig-prof-%d", suffix),
		Format:  "anthropic",
		Model:   "claude-test",
		BaseURL: mockLLM.URL,
		APIKey:  "sk-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateAgent(boundKey, "bound trigger agent", ""); err != nil {
		t.Fatal(err)
	}
	if err := pg.SetAgentLLMProfile(boundKey, &profID); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateAgent(unboundKey, "unbound trigger agent", ""); err != nil {
		t.Fatal(err)
	}

	waitAgentIdle := func(key string) {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			s.queueMu.Lock()
			active := s.triggerActive[key]
			queued := len(s.triggerQ[key])
			s.queueMu.Unlock()
			if active == 0 && queued == 0 {
				return
			}
			select {
			case <-ticker.C:
			case <-deadline.C:
				t.Fatalf("trigger queue for %s did not drain (active=%d queued=%d)", key, active, queued)
			}
		}
	}

	t.Cleanup(func() {
		cancel()
		waitAgentIdle(boundKey)
		waitAgentIdle(unboundKey)
		_, _ = pg.Exec(`DELETE FROM conversations WHERE agent_key IN ($1, $2)`, boundKey, unboundKey)
		_ = pg.DeleteAgent(boundKey)
		_ = pg.DeleteAgent(unboundKey)
		_ = pg.DeleteProfile(profID)
		pg.Close()
	})

	// Global chatAgent is intentionally nil.
	if s.chatAgentRef() != nil {
		t.Fatal("expected nil global chatAgentRef")
	}

	// 1. Unbound agent with neither global nor per-agent LLM must be ignored cleanly
	// without panicking or leaving dirty queue state, and direct runTriggeredRun
	// must also return cleanly without creating an orphan conversation.
	s.StartTriggeredRun(unboundKey, "unbound fire", "should skip", 0, false, "", "")
	s.runTriggeredRun(triggeredRun{agentKey: unboundKey, title: "direct unbound", message: "should skip"})

	var unboundConvs int
	if err := pg.QueryRow(`SELECT count(*) FROM conversations WHERE agent_key=$1`, unboundKey).Scan(&unboundConvs); err != nil {
		t.Fatal(err)
	}
	if unboundConvs != 0 {
		t.Fatalf("unbound agent created %d conversations, want 0", unboundConvs)
	}
	s.queueMu.Lock()
	if s.triggerActive[unboundKey] != 0 || len(s.triggerQ[unboundKey]) != 0 {
		s.queueMu.Unlock()
		t.Fatalf("unbound queue state corrupted: active=%d q=%d", s.triggerActive[unboundKey], len(s.triggerQ[unboundKey]))
	}
	s.queueMu.Unlock()

	// 2. Profile-bound agent must enqueue and attempt the run despite nil global chatAgent.
	s.StartTriggeredRun(boundKey, "bound fire", "run with bound profile", 0, false, "", "")
	waitAgentIdle(boundKey)

	var boundConvs int
	if err := pg.QueryRow(`SELECT count(*) FROM conversations WHERE agent_key=$1`, boundKey).Scan(&boundConvs); err != nil {
		t.Fatal(err)
	}
	if boundConvs != 1 {
		t.Fatalf("bound agent conversations=%d, want 1", boundConvs)
	}
	if llmCalls.Load() == 0 {
		t.Fatal("bound agent did not attempt LLM request via its bound profile")
	}
}
