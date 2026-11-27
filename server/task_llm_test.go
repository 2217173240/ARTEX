package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmpool"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

type scriptedLLMProvider struct {
	events []llm.StreamEvent
	err    error
	calls  int
}

func (p *scriptedLLMProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.calls++
		for _, event := range p.events {
			if !yield(event, nil) {
				return
			}
		}
		if p.err != nil {
			yield(llm.StreamEvent{}, p.err)
		}
	}
}

func (p *scriptedLLMProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return accumulateStreamForTest(ctx, p.Stream, req)
}

// accumulateStreamForTest drains a mock's Stream to satisfy the non-streaming
// Complete method.
func accumulateStreamForTest(ctx context.Context, stream func(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error], req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	acc := llm.NewAccumulator()
	for ev, err := range stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		acc.Add(ev)
	}
	return acc.Message(), acc.StopReason, acc.Usage, nil
}

// withZeroRetryBackoff sets the same-provider retry backoff to zero for the
// duration of a (serial) test and returns a restore func for defer.
func withZeroRetryBackoff() func() {
	prev := sameProviderRetryBackoff
	sameProviderRetryBackoff = func(int) time.Duration { return 0 }
	return func() { sameProviderRetryBackoff = prev }
}

// flakyThenOKProvider fails its first failCount stream attempts pre-commit
// (emitting failEvents, or just a non-committing SEMessageStart by default,
// mirroring a gateway that returns 200 then drops), then serves okEvents.
type flakyThenOKProvider struct {
	failCount  int
	failErr    error
	failEvents []llm.StreamEvent
	okEvents   []llm.StreamEvent
	calls      int
}

func (p *flakyThenOKProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.calls++
		if p.calls <= p.failCount {
			events := p.failEvents
			if events == nil {
				events = []llm.StreamEvent{{Type: llm.SEMessageStart}}
			}
			for _, event := range events {
				if !yield(event, nil) {
					return
				}
			}
			yield(llm.StreamEvent{}, p.failErr)
			return
		}
		for _, ev := range p.okEvents {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func (p *flakyThenOKProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return accumulateStreamForTest(ctx, p.Stream, req)
}

func collectTaskLLMStream(seq iter.Seq2[llm.StreamEvent, error]) ([]llm.StreamEvent, error) {
	var events []llm.StreamEvent
	var streamErr error
	for event, err := range seq {
		if err != nil {
			streamErr = err
			break
		}
		events = append(events, event)
	}
	return events, streamErr
}

func TestIsQuotaExhaustedError(t *testing.T) {
	t.Parallel()
	positive := []string{
		`openai: code=insufficient_quota status=429`,
		`provider error: quota_exceeded`,
		`grpc code=RESOURCE_EXHAUSTED`,
		`You exceeded your current quota, please check your plan and billing details.`,
		`billing_hard_limit_reached`,
		`billing_not_active`,
		`HTTP status 402 Payment Required`,
		`账户余额不足，请充值`,
		`credit balance is too low`,
		`openai: status 429: {"type":"error","error":{"type":"GoUsageLimitError","message":"OpenCode Go usage limit reached (5-hour, resets in 2h 9m)."}}`,
		`openai: status 402: {"error":{"code":"usage_limit_reached"}}`,
	}
	for _, message := range positive {
		if !isQuotaExhaustedError(errors.New(message)) {
			t.Errorf("expected quota classification for %q", message)
		}
	}

	negative := []string{
		`HTTP status 429: rate limit exceeded`,
		`RESOURCE_EXHAUSTED: rate limit exceeded`,
		`HTTP status 429: quota exceeded for quota metric GenerateRequestsPerMinutePerProjectPerBaseModel`,
		`RESOURCE_EXHAUSTED: TPM quota exceeded`,
		`HTTP status 429: rate_limit_error: usage limit reached`,
		`HTTP status 429: too many requests: usage limit exceeded`,
		`HTTP status 401: invalid api key`,
		`HTTP status 401: insufficient_quota`,
		`HTTP status 403: forbidden`,
		`HTTP status 403: billing_hard_limit_reached`,
		`HTTP status 500: internal server error`,
		`HTTP status 503: quota_exceeded`,
		`dial tcp: network is unreachable`,
		`context length exceeded`,
		`invalid request parameter`,
	}
	for _, message := range negative {
		if isQuotaExhaustedError(errors.New(message)) {
			t.Errorf("unexpected quota classification for %q", message)
		}
	}
	if isQuotaExhaustedError(nil) {
		t.Error("nil error must not be classified as quota exhaustion")
	}
}

func TestTaskDTOUsesEmptyArrays(t *testing.T) {
	t.Parallel()
	dto := taskDTO(&Task{ID: "7"}, "created")
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"source_task_ids", "llm_profile_ids"} {
		value, ok := payload[field].([]any)
		if !ok || len(value) != 0 {
			t.Fatalf("%s must serialize as [], payload=%s", field, raw)
		}
	}
}

func TestApplyTaskArchiveBlocker(t *testing.T) {
	t.Parallel()
	dto := taskDTO(&Task{ID: "7"}, "paused")
	applyTaskArchiveBlocker(&dto, map[int64]int64{7: 11})
	if dto.ArchiveBlockedBy != "11" {
		t.Fatalf("archive blocker=%q, want 11", dto.ArchiveBlockedBy)
	}
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"archive_blocked_by_task_id":"11"`) {
		t.Fatalf("archive blocker missing from payload: %s", raw)
	}
}

func TestTaskLLMStateSnapshotIsConcurrentAndDetached(t *testing.T) {
	t.Parallel()
	task := &Task{ID: "7"}
	var wg sync.WaitGroup
	for writer := 0; writer < 2; writer++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				id := int64(offset*1000 + i + 1)
				task.setLLMState(&id, &id, []int64{id, id + 1}, int64(i), "ready", "")
			}
		}(writer)
	}
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				state := task.llmStateSnapshot()
				if len(state.ProfileIDs) > 0 {
					state.ProfileIDs[0] = -1
				}
				_ = taskDTO(task, "running")
			}
		}()
	}
	wg.Wait()
	state := task.llmStateSnapshot()
	if len(state.ProfileIDs) != 2 || state.ProfileIDs[0] <= 0 {
		t.Fatalf("snapshot mutation leaked into task state: %+v", state)
	}
}

func TestTaskLLMStateRejectsOlderRevision(t *testing.T) {
	t.Parallel()
	task := &Task{ID: "7"}
	current := int64(22)
	stale := int64(11)
	if !task.setLLMState(&current, &current, []int64{current}, 2, "ready", "") {
		t.Fatal("initial LLM state was rejected")
	}
	if task.setLLMState(&stale, &stale, []int64{stale}, 1, "ready", "stale") {
		t.Fatal("older LLM snapshot was accepted")
	}
	state := task.llmStateSnapshot()
	if state.ChainRevision != 2 || state.ActiveID == nil || *state.ActiveID != current ||
		len(state.ProfileIDs) != 1 || state.ProfileIDs[0] != current || state.FailoverReason != "" {
		t.Fatalf("older LLM snapshot overwrote current state: %+v", state)
	}
}

func TestTaskLLMStreamRetriesPreStreamQuotaOnNextProfile(t *testing.T) {
	t.Parallel()
	quota := errors.New("openai: status 402: insufficient_quota")
	first := &scriptedLLMProvider{err: quota}
	second := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "fallback"}}}
	active := int64(11)
	transitions := 0
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			if active == 11 {
				return taskLLMSelection{profileID: 11, provider: first}, nil
			}
			return taskLLMSelection{profileID: 22, provider: second}, nil
		},
		exhaust: func(selection taskLLMSelection, _ error) (db.TaskLLMTransition, error) {
			if selection.profileID != 11 {
				t.Fatalf("unexpected exhausted profile %d", selection.profileID)
			}
			active = 22
			next := active
			return db.TaskLLMTransition{PreviousProfileID: 11, NextProfileID: &next, Advanced: true}, nil
		},
		transition: func(taskLLMSelection, db.TaskLLMTransition, error) { transitions++ },
	}

	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Text != "fallback" {
		t.Fatalf("unexpected fallback events: %+v", events)
	}
	if first.calls != 1 || second.calls != 1 || transitions != 1 {
		t.Fatalf("calls first=%d second=%d transitions=%d", first.calls, second.calls, transitions)
	}
}

func TestTaskLLMCompleteFailsOverOnOpenCodeGoUsageLimit(t *testing.T) {
	t.Parallel()
	var firstCalls, secondCalls atomic.Int32
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"GoUsageLimitError","message":"OpenCode Go usage limit reached (5-hour, resets in 2h 9m). To keep going now, turn on Extra Usage or upgrade to Go Plus at https://opencode.ai/console/wrk_01KZNXXXXXXXXXXXXXXXDJFJH/go."},"metadata":{"workspace":"wrk_01KZNXXXXXXXXXXXXXXXXTEDJFJH","limitName":"5 hour"}}`)
	}))
	defer firstServer.Close()

	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"fallback-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer secondServer.Close()

	firstProv, err := agent.ConfigFrom("openai", "test-model", firstServer.URL, "key-1", "").NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	secondProv, err := agent.ConfigFrom("openai", "test-model", secondServer.URL, "key-2", "").NewProvider()
	if err != nil {
		t.Fatal(err)
	}

	active := int64(11)
	transitions := 0
	var exhaustCause error
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			if active == 11 {
				return taskLLMSelection{profileID: 11, provider: firstProv}, nil
			}
			return taskLLMSelection{profileID: 22, provider: secondProv}, nil
		},
		exhaust: func(selection taskLLMSelection, cause error) (db.TaskLLMTransition, error) {
			if selection.profileID != 11 {
				t.Fatalf("unexpected exhausted profile %d", selection.profileID)
			}
			exhaustCause = cause
			active = 22
			next := active
			return db.TaskLLMTransition{PreviousProfileID: 11, NextProfileID: &next, Advanced: true}, nil
		},
		transition: func(taskLLMSelection, db.TaskLLMTransition, error) { transitions++ },
	}

	msg, _, _, err := completeTaskLLM(context.Background(), "9", llm.CompletionRequest{
		Messages: []llm.Message{llm.UserText("ping")},
	}, hooks)
	if err != nil {
		t.Fatalf("expected failover to succeed, got %v", err)
	}
	if got := msg.Text(); got != "fallback-ok" {
		t.Fatalf("reply=%q, want fallback-ok", got)
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 || transitions != 1 {
		t.Fatalf("calls first=%d second=%d transitions=%d, want 1/1/1", firstCalls.Load(), secondCalls.Load(), transitions)
	}
	if exhaustCause == nil || !strings.Contains(exhaustCause.Error(), "status 402") {
		t.Fatalf("exhaustCause=%v, want normalized status 402", exhaustCause)
	}
}

func TestTaskLLMStreamRetriesAfterNonContentStartEvent(t *testing.T) {
	t.Parallel()
	quota := errors.New("anthropic: status 402: insufficient credit balance")
	first := &scriptedLLMProvider{
		events: []llm.StreamEvent{{Type: llm.SEMessageStart}},
		err:    quota,
	}
	second := &scriptedLLMProvider{events: []llm.StreamEvent{
		{Type: llm.SEMessageStart},
		{Type: llm.SETextDelta, Text: "fallback"},
	}}
	active := int64(11)
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			if active == 11 {
				return taskLLMSelection{profileID: 11, provider: first}, nil
			}
			return taskLLMSelection{profileID: 22, provider: second}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			active = 22
			next := active
			return db.TaskLLMTransition{PreviousProfileID: 11, NextProfileID: &next, Advanced: true}, nil
		},
	}

	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != llm.SEMessageStart || events[1].Text != "fallback" {
		t.Fatalf("non-content event from failed attempt leaked or fallback missing: %+v", events)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("calls first=%d second=%d", first.calls, second.calls)
	}
}

func TestTaskLLMStreamRetriesStalePreStreamFailureAgainstReplacementChain(t *testing.T) {
	t.Parallel()
	first := &scriptedLLMProvider{err: errors.New("status 402: insufficient_quota")}
	second := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "replacement"}}}
	current := first
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			profileID := int64(11)
			if current == second {
				profileID = 22
			}
			return taskLLMSelection{profileID: profileID, provider: current}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			current = second
			return db.TaskLLMTransition{PreviousProfileID: 11, Stale: true}, nil
		},
	}

	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if err != nil || len(events) != 1 || events[0].Text != "replacement" {
		t.Fatalf("stale call did not continue on replacement chain: events=%+v err=%v", events, err)
	}
}

func TestTaskLLMStreamDoesNotSwitchForOrdinaryErrors(t *testing.T) {
	// Not parallel: overrides the package-level backoff so the retryable cases
	// don't sleep. Serial tests never overlap the parallel batch, so this is safe.
	defer withZeroRetryBackoff()()
	tests := []string{
		"openai: status 429: rate limit exceeded",
		"openai: status 401: invalid api key",
		"openai: status 500: internal server error",
		"dial tcp: network is unreachable",
	}
	for _, message := range tests {
		t.Run(message, func(t *testing.T) {
			provider := &scriptedLLMProvider{err: errors.New(message)}
			exhausted := 0
			hooks := taskLLMStreamHooks{
				current: func() (taskLLMSelection, error) {
					return taskLLMSelection{profileID: 11, provider: provider}, nil
				},
				exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
					exhausted++
					return db.TaskLLMTransition{}, nil
				},
			}
			_, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
			if err == nil || err.Error() != message {
				t.Fatalf("error=%v, want %q", err, message)
			}
			if exhausted != 0 {
				t.Fatalf("ordinary error changed failover state %d times", exhausted)
			}
		})
	}
}

func TestTaskLLMStreamAdvancesAfterMidStreamQuotaWithoutReplay(t *testing.T) {
	t.Parallel()
	quota := errors.New("anthropic: status 402: credit balance is too low")
	first := &scriptedLLMProvider{
		events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "partial"}},
		err:    quota,
	}
	second := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "next-call"}}}
	active := int64(11)
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			if active == 11 {
				return taskLLMSelection{profileID: 11, provider: first}, nil
			}
			return taskLLMSelection{profileID: 22, provider: second}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			active = 22
			next := active
			return db.TaskLLMTransition{PreviousProfileID: 11, NextProfileID: &next, Advanced: true}, nil
		},
	}

	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if len(events) != 1 || events[0].Text != "partial" || err == nil {
		t.Fatalf("mid-stream result events=%+v err=%v", events, err)
	}
	if first.calls != 1 || second.calls != 0 {
		t.Fatalf("mid-stream call was replayed: first=%d second=%d", first.calls, second.calls)
	}
	if !errors.Is(err, quota) || retryableWorkerModelError(harness.ReasonModelError, err) || isTaskLLMChainExhausted(err) {
		t.Fatalf("partial quota failure must preserve its cause and stop worker replay without exhausting the chain: %T %v", err, err)
	}

	nextEvents, nextErr := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if nextErr != nil || len(nextEvents) != 1 || nextEvents[0].Text != "next-call" || second.calls != 1 {
		t.Fatalf("next call did not use fallback: events=%+v err=%v calls=%d", nextEvents, nextErr, second.calls)
	}
}

func TestTaskLLMStreamMarkFailureAfterToolStartSkipsWorkerReplay(t *testing.T) {
	t.Parallel()
	quota := errors.New("openai: status 402: insufficient_quota")
	markFailure := errors.New("database temporarily unavailable")
	provider := &scriptedLLMProvider{
		events: []llm.StreamEvent{{Type: llm.SEToolUseStart}},
		err:    quota,
	}
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{profileID: 11, provider: provider}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			return db.TaskLLMTransition{}, markFailure
		},
	}

	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if len(events) != 1 || events[0].Type != llm.SEToolUseStart || err == nil {
		t.Fatalf("committed tool stream events=%+v err=%v", events, err)
	}
	if !isTaskLLMRuntimeError(err) {
		t.Fatalf("committed mark failure must be router-classified: %T %v", err, err)
	}
	if !errors.Is(err, quota) || !errors.Is(err, markFailure) {
		t.Fatalf("provider or persistence cause was lost: %v", err)
	}
	if retryableWorkerModelError(harness.ReasonModelError, err) {
		t.Fatal("worker must not replay after a committed tool stream when quota persistence fails")
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls=%d, want 1", provider.calls)
	}
}

func TestTaskLLMCompleteMarkFailureRetainsCauses(t *testing.T) {
	t.Parallel()
	quota := errors.New("openai: status 402: insufficient_quota")
	markFailure := errors.New("database temporarily unavailable")
	provider := &scriptedLLMProvider{err: quota}
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{profileID: 11, provider: provider}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			return db.TaskLLMTransition{}, markFailure
		},
	}
	_, _, _, err := completeTaskLLM(t.Context(), "7", llm.CompletionRequest{}, hooks)
	if !errors.Is(err, quota) || !errors.Is(err, markFailure) {
		t.Fatalf("provider or persistence cause was lost: %v", err)
	}
}

func TestTaskLLMRuntimeErrorSkipsWorkerReplay(t *testing.T) {
	partialStreamErr := &taskLLMError{
		taskID:         "7",
		chainExhausted: false,
		cause:          errors.New("insufficient_quota after partial output"),
	}
	if retryableWorkerModelError(harness.ReasonModelError, partialStreamErr) {
		t.Fatal("worker must not replay an intent after the task router handled a mid-stream quota error")
	}
	if !retryableWorkerModelError(harness.ReasonModelError, errors.New("temporary network error")) {
		t.Fatal("ordinary model errors should retain the existing worker retry behavior")
	}
}

// A transient pre-commit stream failure (200-then-drop / overloaded / 5xx) is
// retried on the SAME provider, without switching profiles, and succeeds once the
// blip clears — instead of surfacing as a model_error.
func TestTaskLLMStreamRetriesTransientPreCommitOnSameProvider(t *testing.T) {
	defer withZeroRetryBackoff()()
	provider := &flakyThenOKProvider{
		failCount: 2, // fail twice, succeed on the 3rd attempt (initial + 2 retries)
		failErr:   errors.New("anthropic: overloaded_error"),
		okEvents:  []llm.StreamEvent{{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "ok"}},
	}
	exhausted := 0
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{profileID: 11, provider: provider}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			exhausted++
			return db.TaskLLMTransition{}, nil
		},
	}
	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if err != nil {
		t.Fatalf("transient pre-commit failure should have been retried to success, got %v", err)
	}
	if provider.calls != 3 {
		t.Fatalf("provider calls=%d, want 3 (1 initial + 2 retries)", provider.calls)
	}
	if exhausted != 0 {
		t.Fatalf("same-provider retry must not change failover state (exhausted=%d)", exhausted)
	}
	if len(events) == 0 || events[len(events)-1].Text != "ok" {
		t.Fatalf("recovered stream missing its committed output: %+v", events)
	}
}

// Once retries are exhausted, the transient error is surfaced (becoming a
// model_error upstream) after exactly sameProviderStreamRetries+1 attempts.
func TestTaskLLMStreamSurfacesTransientAfterRetriesExhausted(t *testing.T) {
	defer withZeroRetryBackoff()()
	provider := &flakyThenOKProvider{
		failCount: 99, // never recovers
		failErr:   errors.New("dial tcp: connection reset by peer"),
	}
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{profileID: 11, provider: provider}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			return db.TaskLLMTransition{}, nil
		},
	}
	_, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if err == nil {
		t.Fatal("persistent transient failure must still surface an error")
	}
	if provider.calls != sameProviderStreamRetries+1 {
		t.Fatalf("provider calls=%d, want %d", provider.calls, sameProviderStreamRetries+1)
	}
}

// A deterministic 4xx rejection is NOT retried on the same provider — replaying
// the identical request everywhere fails the same way.
func TestTaskLLMStreamDoesNotRetryDeterministicRejection(t *testing.T) {
	defer withZeroRetryBackoff()()
	provider := &flakyThenOKProvider{
		failCount: 99,
		failErr:   errors.New("anthropic: status 400: messages.1: invalid request"),
	}
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{profileID: 11, provider: provider}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			return db.TaskLLMTransition{}, nil
		},
	}
	_, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if err == nil {
		t.Fatal("expected the deterministic rejection to surface")
	}
	if provider.calls != 1 {
		t.Fatalf("deterministic 4xx must not be retried: provider calls=%d, want 1", provider.calls)
	}
}

func TestEngineReadyForTaskOverride(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	if e.Ready() {
		t.Fatal("engine without a global provider must not be globally ready")
	}
	e.SetAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		return &agent.Planner{}, &agent.Worker{}
	})
	if !e.ReadyFor(&Task{ID: "7"}) {
		t.Fatal("task-specific provider chain must make that task ready")
	}
}

func TestResolvedTaskStatusKeepsExhaustedStartedTaskRunning(t *testing.T) {
	t.Parallel()
	engine := NewEngine(nil)
	engine.started.Store("7", true)
	task := &Task{ID: "7", Status: "running"}
	profileID := int64(11)
	task.setLLMState(&profileID, nil, []int64{profileID}, 1, "chain_exhausted", "quota exhausted")
	s := &Server{engine: engine}
	if got := s.resolvedTaskStatus(task); got != "running" {
		t.Fatalf("exhausted started status=%q, want running", got)
	}
	task.Paused = true
	if got := s.resolvedTaskStatus(task); got != "paused" {
		t.Fatalf("paused exhausted status=%q, want paused", got)
	}
}

// A role with no Agent binding falls through to the task chain, then to global —
// and an exhausted chain stays a hard stop instead of silently borrowing global.
func TestTaskRuntimeAvailableUnboundRoleFollowsChainThenGlobal(t *testing.T) {
	t.Parallel()
	// A nil pg means effectiveProfileForAgent finds no binding for any role, so
	// every role here resolves through the chain/global levels.
	s := &Server{m: &Manager{}}
	task := &Task{ID: "7"}

	if s.taskRuntimeAvailable(task, "worker") {
		t.Fatal("no binding, no chain and no global provider must not be runnable")
	}
	s.llmOn, s.llmProv = true, &scriptedLLMProvider{}
	if !s.taskRuntimeAvailable(task, "worker") {
		t.Fatal("global provider must serve a role with no binding and no chain")
	}
	if s.taskRuntimeAvailable(task) {
		t.Fatal("a task with no roles requested must not report as runnable")
	}

	profileID := int64(11)
	task.setLLMState(&profileID, nil, []int64{profileID}, 1, "chain_exhausted", "quota exhausted")
	if s.taskRuntimeAvailable(task, "worker") {
		t.Fatal("exhausted chain must not fall back to the global provider")
	}
}

func TestAuthoritativeTaskResolverDoesNotFallBackWhenChainUnavailable(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		return &agent.Planner{}, &agent.Worker{}
	})
	if !e.ReadyFor(&Task{ID: "7"}) {
		t.Fatal("an available authoritative chain should make the task ready")
	}
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		return nil, nil
	})
	if e.ReadyFor(&Task{ID: "7"}) {
		t.Fatal("unavailable authoritative task chain must not fall back to global provider")
	}
}

func TestTaskLLMStreamStopsWhenChainExhausted(t *testing.T) {
	t.Parallel()
	provider := &scriptedLLMProvider{err: errors.New("billing_not_active")}
	transitions := 0
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{profileID: 11, provider: provider}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			return db.TaskLLMTransition{PreviousProfileID: 11, Advanced: true, ChainExhausted: true}, nil
		},
		transition: func(taskLLMSelection, db.TaskLLMTransition, error) { transitions++ },
	}

	_, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if !isTaskLLMChainExhausted(err) {
		t.Fatalf("expected chain-exhausted error, got %v", err)
	}
	if provider.calls != 1 || transitions != 1 {
		t.Fatalf("calls=%d transitions=%d", provider.calls, transitions)
	}
}

func TestProfileDeleteRestoresQuotaBlockedIntentWhenFallbackAvailable(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()

	profileID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name:   fmt.Sprintf("delete-quota-fallback-%d", time.Now().UnixNano()),
		Format: "openai",
		Model:  "test-model",
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTaskWithOptions("quota fallback", "resume intent", db.TaskCreateOptions{
		LLMProfileIDs: []int64{profileID},
	})
	if err != nil {
		_ = m.pg.DeleteProfile(profileID)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
		_ = m.pg.DeleteProfile(profileID)
	})

	intentID, err := task.Store.AddIntent(map[string]any{"action": "retry after fallback"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := parseTaskID(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if transition, err := m.pg.MarkTaskLLMProfileQuotaExhausted(taskID, profileID, "insufficient_quota"); err != nil || !transition.ChainExhausted {
		t.Fatalf("exhaust task chain: transition=%+v err=%v", transition, err)
	}
	if err := task.Store.SetIntentBlockedReason(intentID, db.IntentBlockedLLMQuota); err != nil {
		t.Fatal(err)
	}
	task.Paused = true
	if err := m.SetTaskPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}

	// Removing the final explicit entry restores the legacy/global fallback. The
	// post-delete sync must reopen only quota-blocked work without unpausing it.
	if err := m.pg.DeleteProfile(profileID); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		m:             m,
		llmOn:         true,
		llmProv:       &scriptedLLMProvider{},
		provByProfile: map[int64]*provEntry{},
	}
	s.restoreTasksAfterProfileDelete(m.pg)

	node, err := task.Store.GetNode(intentID)
	if err != nil {
		t.Fatal(err)
	}
	if node == nil || node.State != "open" || node.BlockedReason != "" {
		t.Fatalf("quota-blocked intent was not reopened: %+v", node)
	}
	if !task.Paused {
		t.Fatal("profile deletion must not resume a paused task")
	}
	state := task.llmStateSnapshot()
	if len(state.ProfileIDs) != 0 || state.FailoverState != "default" {
		t.Fatalf("task did not return to fallback state: %+v", state)
	}
}

// ---- 单 profile 路径向进程级熔断器上报成败（BaaRai/ARTEX#257 适配） ----
// Pool 被绕过时(任务链上只有一个可用 profile),completeTaskLLM/streamTaskLLM 是
// 唯一知道成败的地方;recordPass/recordFailure 钩子把结果送回 s.llmHealth,使
// /system/llm/ 的健康徽标不因单 profile 任务而失真。守卫(profileID==0 不入
// llm_profile_health、ctx 取消与确定性 400 不动状态)住在 streamHooks() 接线里,
// 因此取消/全局路径的用例直接消费真实接线。

// 单 profile 流成功:recordPass(profileID) 恰好一次,recordFailure 零次。
func TestTaskLLMStreamRecordsPassOnSingleProfileSuccess(t *testing.T) {
	t.Parallel()
	provider := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "ok"}}}
	var passes, failures []int64
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{profileID: 11, provider: provider}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			t.Fatal("exhaust must not run for an ordinary success")
			return db.TaskLLMTransition{}, nil
		},
		recordPass:    func(profileID int64) { passes = append(passes, profileID) },
		recordFailure: func(context.Context, int64, error) { failures = append(failures, -1) },
	}
	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if err != nil || len(events) != 1 || events[0].Text != "ok" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if len(passes) != 1 || passes[0] != 11 {
		t.Fatalf("recordPass calls=%v, want exactly one with profile 11", passes)
	}
	if len(failures) != 0 {
		t.Fatalf("recordFailure must not run on success: %v", failures)
	}
}

// 单 profile 普通错误(非额度,这里用确定性 404 避开同 provider 重试):透传给调用方,
// 但 recordFailure 恰好一次且 profileID 正确,recordPass 零次。
func TestTaskLLMStreamRecordsFailureOnSingleProfileError(t *testing.T) {
	t.Parallel()
	provider := &scriptedLLMProvider{err: errors.New("openai: status 404: model not found")}
	var passes []int64
	var failures []struct {
		profileID int64
		err       error
	}
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{profileID: 11, provider: provider}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			t.Fatal("ordinary error must not exhaust the chain")
			return db.TaskLLMTransition{}, nil
		},
		recordPass: func(profileID int64) { passes = append(passes, profileID) },
		recordFailure: func(_ context.Context, profileID int64, err error) {
			failures = append(failures, struct {
				profileID int64
				err       error
			}{profileID, err})
		},
	}
	_, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("err=%v, want the passthrough 404", err)
	}
	if len(passes) != 0 {
		t.Fatalf("recordPass must not run on failure: %v", passes)
	}
	if len(failures) != 1 || failures[0].profileID != 11 || failures[0].err != err {
		t.Fatalf("recordFailure calls=%+v, want exactly one with profile 11 and the surfaced error", failures)
	}
}

// ctx 取消:调用点仍会走 recordFailure 钩子,但接线层的 ShouldFailover 守卫必须把
// 这类失败挡在熔断器之外(不写 Trip、不改状态)。
func TestTaskLLMStreamCancelDoesNotReachBreaker(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if llmpool.ShouldFailover(ctx, ctx.Err()) {
		t.Fatal("sanity: a cancelled ctx with context.Canceled must classify as non-failover")
	}
	provider := &scriptedLLMProvider{err: ctx.Err()}
	trips := 0
	registry := llmpool.NewRegistry(func(int64, llmpool.State) { trips++ }, nil)
	wired := (&taskLLMRuntime{s: &Server{llmHealth: registry}}).streamHooks()
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{profileID: 11, provider: provider}, nil
		},
		exhaust:       func(taskLLMSelection, error) (db.TaskLLMTransition, error) { return db.TaskLLMTransition{}, nil },
		recordPass:    wired.recordPass,
		recordFailure: wired.recordFailure,
	}
	_, err := collectTaskLLMStream(streamTaskLLM(ctx, "7", llm.CompletionRequest{}, hooks))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	if trips != 0 || len(registry.Snapshot()) != 0 {
		t.Fatalf("cancelled stream must not touch the breaker: trips=%d state=%+v", trips, registry.Snapshot())
	}
}

// profileID==0(绑定/全局路径,不入 llm_profile_health):成功与失败都不产生熔断器写入。
func TestTaskLLMStreamGlobalPathNeverReachesBreaker(t *testing.T) {
	t.Parallel()
	trips := 0
	forgets := 0
	registry := llmpool.NewRegistry(func(int64, llmpool.State) { trips++ }, func(int64) { forgets++ })
	wired := (&taskLLMRuntime{s: &Server{llmHealth: registry}}).streamHooks()
	for _, tc := range []struct {
		name     string
		provider *scriptedLLMProvider
	}{
		{"success", &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "global"}}}},
		{"failure", &scriptedLLMProvider{err: errors.New("openai: status 404: model not found")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hooks := taskLLMStreamHooks{
				current: func() (taskLLMSelection, error) {
					return taskLLMSelection{profileID: 0, provider: tc.provider}, nil
				},
				exhaust:       func(taskLLMSelection, error) (db.TaskLLMTransition, error) { return db.TaskLLMTransition{}, nil },
				recordPass:    wired.recordPass,
				recordFailure: wired.recordFailure,
			}
			if _, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks)); tc.name == "failure" && err == nil {
				t.Fatal("failure case must surface its error")
			}
		})
	}
	if trips != 0 || forgets != 0 || len(registry.Snapshot()) != 0 {
		t.Fatalf("profile 0 must never reach the breaker: trips=%d forgets=%d state=%+v", trips, forgets, registry.Snapshot())
	}
}

// completeTaskLLM 同样上报:成功一次 recordPass;普通错误(非额度、非取消)透传前
// 一次 recordFailure。404 是确定性错误,不触发同 provider 重试,测试可并行。
func TestTaskLLMCompleteRecordsPassAndFailure(t *testing.T) {
	t.Parallel()
	run := func(provider *scriptedLLMProvider) (passes []int64, failures []int64, err error) {
		hooks := taskLLMStreamHooks{
			current: func() (taskLLMSelection, error) {
				return taskLLMSelection{profileID: 11, provider: provider}, nil
			},
			exhaust:       func(taskLLMSelection, error) (db.TaskLLMTransition, error) { return db.TaskLLMTransition{}, nil },
			recordPass:    func(profileID int64) { passes = append(passes, profileID) },
			recordFailure: func(context.Context, int64, error) { failures = append(failures, 11) },
		}
		_, _, _, err = completeTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks)
		return passes, failures, err
	}

	passes, failures, err := run(&scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "ok"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(passes) != 1 || passes[0] != 11 || len(failures) != 0 {
		t.Fatalf("complete success: passes=%v failures=%v, want one pass on 11 and no failure", passes, failures)
	}

	passes, failures, err = run(&scriptedLLMProvider{err: errors.New("openai: status 404: model not found")})
	if err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("complete failure err=%v, want the passthrough 404", err)
	}
	if len(passes) != 0 || len(failures) != 1 {
		t.Fatalf("complete failure: passes=%v failures=%v, want no pass and exactly one failure", passes, failures)
	}
}

// streamHooks() 的真实接线:recordPass 清除既有 Trip 状态,recordFailure 对确定性
// hard error(402)立即 Open;瞬时错误(429)只累计 fails,不提前熔断。Server 缺少
// llmHealth 或 runtime 无 Server 时静默 no-op,不 panic。
func TestTaskLLMStreamHooksWiredToBreakerRegistry(t *testing.T) {
	t.Parallel()
	registry := llmpool.NewRegistry(nil, nil)
	hooks := (&taskLLMRuntime{s: &Server{llmHealth: registry}}).streamHooks()

	hooks.recordFailure(context.Background(), 11, errors.New("anthropic: status 402: credit balance is too low"))
	if !registry.IsOpen(11) {
		t.Fatal("hard failure must open the breaker immediately")
	}
	hooks.recordPass(11)
	if registry.IsOpen(11) || len(registry.Snapshot()) != 0 {
		t.Fatalf("pass must clear the tripped state: %+v", registry.Snapshot())
	}

	hooks.recordFailure(context.Background(), 11, errors.New("openai: status 429: rate limit exceeded"))
	if st := registry.Get(11); st.Open() || st.Fails != 1 {
		t.Fatalf("transient failure must only degrade: %+v", st)
	}

	// 守卫路径:profileID==0、nil error、ctx 取消都不写状态。
	empty := llmpool.NewRegistry(nil, nil)
	guarded := (&taskLLMRuntime{s: &Server{llmHealth: empty}}).streamHooks()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	guarded.recordPass(0)
	guarded.recordFailure(context.Background(), 0, errors.New("openai: status 500: down"))
	guarded.recordFailure(context.Background(), 11, nil)
	guarded.recordFailure(cancelled, 11, errors.New("openai: status 500: down"))
	if len(empty.Snapshot()) != 0 {
		t.Fatalf("guarded calls must be no-ops: %+v", empty.Snapshot())
	}

	// 无 Server / 无 llmHealth 的 runtime:接线为 no-op,不 panic。
	bare := (&taskLLMRuntime{}).streamHooks()
	bare.recordPass(11)
	bare.recordFailure(context.Background(), 11, errors.New("dial tcp: reset"))
	noHealth := (&taskLLMRuntime{s: &Server{}}).streamHooks()
	noHealth.recordPass(11)
	noHealth.recordFailure(context.Background(), 11, errors.New("dial tcp: reset"))
}

func TestIsRetryableStreamErrorCallTimeout(t *testing.T) {
	t.Parallel()
	callTimeout := &agent.CallTimeoutError{
		Provider: "openai",
		Model:    "gpt-5",
		Timeout:  600 * time.Second,
	}
	if isRetryableStreamError(callTimeout) {
		t.Fatal("CallTimeoutError must not be retried on the same provider")
	}
	if isRetryableStreamError(fmt.Errorf("wrapped: %w", callTimeout)) {
		t.Fatal("wrapped CallTimeoutError must not be retried on the same provider")
	}
	if !isRetryableStreamError(errors.New("status 503: upstream overloaded")) {
		t.Fatal("transient 503 must remain retryable")
	}
}
