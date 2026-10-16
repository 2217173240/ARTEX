package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/norma/llm"
)

func TestProfileRequestLimiterSurvivesCacheInvalidation(t *testing.T) {
	s := &Server{}
	s.provCacheMu.Lock()
	limiter := s.requestLimiterForProfileLocked(1)
	if limiter == s.requestLimiterForProfileLocked(2) || limiter == s.requestLimiterForProfileLocked(0) {
		t.Fatal("profile and global histories must be independent")
	}
	s.provCacheMu.Unlock()
	s.invalidateProfileAgents()
	s.provCacheMu.Lock()
	defer s.provCacheMu.Unlock()
	if limiter != s.requestLimiterForProfileLocked(1) {
		t.Fatal("invalidation discarded history")
	}
}

// No database or real model is involved: use the exact construction boundary
// providerForProfile calls after loading its DB snapshot and generation.
func TestProfileRequestLimiterRebuildAndStaleGeneration(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")) != "" {
			t.Error("fixture sent an API key")
		}
		requests.Add(1)
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"message":"fixture failure"}}`)
	}))
	defer upstream.Close()
	cfg := agent.ConfigFrom("openai", "fixture-model", upstream.URL, "", "")
	cfg.Retry.ConnectAttempts = -1
	s := &Server{}
	initial, _, current, err := s.installProfileProvider(1, 0, cfg, nil)
	if err != nil || !current {
		t.Fatalf("initial build: %v, current=%v", err, current)
	}
	req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("fixture")}}
	initial.Complete(context.Background(), req)
	if requests.Load() != 1 {
		t.Fatal("initial request not sent")
	}

	// Simulate an old builder that loaded unlimited config before invalidation
	// and finishes only after the new limited provider was installed.
	release := make(chan struct{})
	finished := make(chan bool, 1)
	go func(oldCfg agent.Config) {
		<-release
		_, _, current, err := s.installProfileProvider(1, 0, oldCfg, nil)
		finished <- !current && err == nil
	}(cfg)
	s.invalidateProfileAgents()
	cfg.RatePerMinute = 1
	rebuilt, _, current, err := s.installProfileProvider(1, 1, cfg, nil)
	if err != nil || !current {
		t.Fatalf("rebuild: %v, current=%v", err, current)
	}
	close(release)
	if !<-finished {
		t.Fatal("stale builder was not rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	_, _, _, err = rebuilt.Complete(ctx, req)
	if !errors.Is(err, context.DeadlineExceeded) || requests.Load() != 1 {
		t.Fatalf("rebuild reset history or stale builder disabled quota: %v, requests=%d", err, requests.Load())
	}
	// Old provider instances also use the newly configured shared quota.
	ctxOld, cancelOld := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancelOld()
	_, _, _, err = initial.Complete(ctxOld, req)
	if !errors.Is(err, context.DeadlineExceeded) || requests.Load() != 1 {
		t.Fatalf("old provider escaped shared quota: %v", err)
	}
	// A different persisted profile can proceed immediately.
	other, _, _, err := s.installProfileProvider(2, 1, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	other.Complete(context.Background(), req)
	if requests.Load() != 2 {
		t.Fatal("independent profile was throttled")
	}
}

func TestProfileRequestLimiterCacheWinnerDoesNotReconfigure(t *testing.T) {
	s := &Server{}
	cfg := agent.ConfigFrom("openai", "fixture", "http://example.invalid", "", "")
	cfg.RatePerMinute = 1
	winner, _, _, err := s.installProfileProvider(1, 0, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Another builder in the same generation must reuse the winner even if its
	// snapshot has malformed settings. It must not reconfigure its limiter.
	cfg.RatePerMinute = -1
	loser, installed, current, err := s.installProfileProvider(1, 0, cfg, nil)
	if err != nil || !current || winner != loser || installed.RatePerMinute != 1 {
		t.Fatalf("lost-race build replaced winner: %v", err)
	}
}
