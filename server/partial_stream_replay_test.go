package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

// toolExecutedEOFProvider waits for a harmless tool to execute before dropping
// the stream. This reproduces the SDK's eager tool execution before the complete
// assistant/tool-result turn has been committed to conversation history.
type toolExecutedEOFProvider struct {
	toolExecuted <-chan struct{}
	calls        int
}

func (p *toolExecutedEOFProvider) Stream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.calls++
		for _, event := range []llm.StreamEvent{
			{Type: llm.SEToolUseStart, ToolID: "probe-1", ToolName: "record_probe"},
			{Type: llm.SEToolInputJSON, Text: `{}`},
			{Type: llm.SEMessageDelta, StopReason: "tool_use"},
		} {
			if !yield(event, nil) {
				return
			}
		}
		select {
		case <-p.toolExecuted:
			yield(llm.StreamEvent{}, io.ErrUnexpectedEOF)
		case <-ctx.Done():
			yield(llm.StreamEvent{}, ctx.Err())
		}
	}
}

func (p *toolExecutedEOFProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return llm.Message{}, "", llm.Usage{}, io.ErrUnexpectedEOF
}

func TestTaskLLMStreamSkipsWorkerReplayAfterToolExecution(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		profileID int64
	}{
		{name: "task_profile", profileID: 11},
		{name: "agent_or_global_fallback", profileID: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			executed := make(chan struct{})
			var toolCalls atomic.Int32
			probe := tool.Build(tool.Spec{
				Name:   "record_probe",
				Schema: map[string]any{"type": "object"},
				Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
					toolCalls.Add(1)
					close(executed)
					return tool.Text("recorded"), nil
				},
			})
			provider := &toolExecutedEOFProvider{toolExecuted: executed}
			hooks := taskLLMStreamHooks{
				current: func() (taskLLMSelection, error) {
					return taskLLMSelection{profileID: tc.profileID, provider: provider}, nil
				},
				exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
					t.Fatal("ordinary EOF must not advance the task profile cursor")
					return db.TaskLLMTransition{}, nil
				},
			}
			var terminal *harness.Terminal
			for event, err := range harness.Query(ctx, harness.QueryInput{
				Messages:       []llm.Message{llm.UserText("record a harmless probe")},
				Tools:          tool.NewRegistry(probe),
				PermissionMode: permission.ModeBypass,
			}, harness.QueryDeps{
				CallModel: func(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
					return streamTaskLLM(ctx, "7", req, hooks)
				},
			}) {
				if err != nil {
					t.Fatalf("harness event error: %v", err)
				}
				if event.Kind == harness.KindResult {
					terminal = event.Terminal
				}
			}
			if terminal == nil || terminal.Reason != harness.ReasonModelError || !errors.Is(terminal.Err, io.ErrUnexpectedEOF) {
				t.Fatalf("terminal=%+v, want model_error preserving unexpected EOF", terminal)
			}
			if got := toolCalls.Load(); got != 1 {
				t.Fatalf("harmless tool executions=%d, want 1 before EOF", got)
			}
			if provider.calls != 1 {
				t.Fatalf("provider calls=%d, want 1 after committed output", provider.calls)
			}
			if retryableWorkerModelError(terminal.Reason, terminal.Err) {
				t.Fatal("worker must not replay the intent after its tool executed before EOF")
			}
		})
	}
}

func TestTaskLLMStreamPreCommitEOFRemainsRetryable(t *testing.T) {
	t.Parallel()
	provider := &scriptedLLMProvider{
		events: []llm.StreamEvent{{Type: llm.SEMessageStart}},
		err:    io.ErrUnexpectedEOF,
	}
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{
				profileID: 11, provider: provider,
				retry: agent.RetryConfig{StreamAttempts: 2, StreamInterval: time.Nanosecond},
			}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			t.Fatal("ordinary EOF must not advance the task profile cursor")
			return db.TaskLLMTransition{}, nil
		},
	}
	events, err := collectTaskLLMStream(streamTaskLLM(t.Context(), "7", llm.CompletionRequest{}, hooks))
	if !errors.Is(err, io.ErrUnexpectedEOF) || !retryableWorkerModelError(harness.ReasonModelError, err) {
		t.Fatalf("pre-commit EOF must retain worker retry behavior: %T %v", err, err)
	}
	if provider.calls != 3 || len(events) != 1 || events[0].Type != llm.SEMessageStart {
		t.Fatalf("provider calls=%d events=%+v, want 3 attempts and only the final non-content event", provider.calls, events)
	}
}

// These prefixes carry no assistant content, so a failed attempt can be
// discarded without leaking its token accounting or duplicating tool execution.
func nonContentTaskLLMEvents() []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 100}},
		{Type: llm.SEMessageDelta, Usage: llm.Usage{OutputTokens: 10}},
		{Type: llm.SEMessageStop},
		{Type: llm.SEThinkingSignature, Text: "discarded-signature"},
		{Type: llm.SETextDelta},
		{Type: llm.SEThinkingDelta},
		{Type: llm.SEToolInputJSON},
	}
}

func TestTaskLLMStreamQuotaAfterMetadataUsesCleanBackup(t *testing.T) {
	t.Parallel()
	for _, prefix := range nonContentTaskLLMEvents() {
		t.Run(string(prefix.Type), func(t *testing.T) {
			first := &scriptedLLMProvider{
				events: []llm.StreamEvent{{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 100}}, prefix},
				err:    errors.New("openai: status 402: insufficient_quota"),
			}
			backupEvents := []llm.StreamEvent{
				{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 20}},
				{Type: llm.SETextDelta, Text: "backup"},
				{Type: llm.SEMessageDelta, StopReason: "end_turn", Usage: llm.Usage{OutputTokens: 2}},
				{Type: llm.SEMessageStop},
			}
			backup := &scriptedLLMProvider{events: backupEvents}
			active, exhausted, transitions := int64(11), 0, 0
			hooks := taskLLMStreamHooks{
				current: func() (taskLLMSelection, error) {
					if active == 11 {
						return taskLLMSelection{profileID: 11, provider: first}, nil
					}
					return taskLLMSelection{profileID: 22, provider: backup}, nil
				},
				exhaust: func(selection taskLLMSelection, cause error) (db.TaskLLMTransition, error) {
					if selection.profileID != 11 || !errors.Is(cause, first.err) {
						t.Fatalf("unexpected exhausted profile=%d cause=%v", selection.profileID, cause)
					}
					exhausted++
					active = 22
					next := active
					return db.TaskLLMTransition{PreviousProfileID: 11, NextProfileID: &next, Advanced: true}, nil
				},
				transition: func(taskLLMSelection, db.TaskLLMTransition, error) { transitions++ },
			}
			events, err := collectTaskLLMStream(streamTaskLLM(t.Context(), "7", llm.CompletionRequest{}, hooks))
			if err != nil || !reflect.DeepEqual(events, backupEvents) {
				t.Fatalf("events=%+v err=%v, want only backup events %+v", events, err, backupEvents)
			}
			acc := llm.NewAccumulator()
			for _, event := range events {
				acc.Add(event)
			}
			if acc.Usage.InputTokens != 20 || acc.Usage.OutputTokens != 2 {
				t.Fatalf("usage=%+v, want only backup usage", acc.Usage)
			}
			if first.calls != 1 || backup.calls != 1 || exhausted != 1 || transitions != 1 || active != 22 {
				t.Fatalf("calls=%d/%d exhausted=%d transitions=%d active=%d", first.calls, backup.calls, exhausted, transitions, active)
			}
		})
	}
}

func TestTaskLLMStreamEOFRetriesDiscardMetadata(t *testing.T) {
	t.Parallel()
	for _, prefix := range nonContentTaskLLMEvents() {
		t.Run(string(prefix.Type), func(t *testing.T) {
			okEvents := []llm.StreamEvent{{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "recovered"}}
			provider := &flakyThenOKProvider{
				failCount: 2, failErr: io.ErrUnexpectedEOF,
				failEvents: []llm.StreamEvent{{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 100}}, prefix},
				okEvents:   okEvents,
			}
			hooks := taskLLMStreamHooks{
				current: func() (taskLLMSelection, error) {
					return taskLLMSelection{
						profileID: 11, provider: provider,
						retry: agent.RetryConfig{StreamAttempts: 2, StreamInterval: time.Nanosecond},
					}, nil
				},
				exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
					t.Fatal("ordinary EOF must not advance the task profile cursor")
					return db.TaskLLMTransition{}, nil
				},
			}
			events, err := collectTaskLLMStream(streamTaskLLM(t.Context(), "7", llm.CompletionRequest{}, hooks))
			if err != nil || provider.calls != 3 || !reflect.DeepEqual(events, okEvents) {
				t.Fatalf("calls=%d events=%+v err=%v, want 3 calls and only successful attempt %+v", provider.calls, events, err, okEvents)
			}
		})
	}
}

func TestTaskLLMStreamCommittedPrefixStopsReplay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		events []llm.StreamEvent
	}{
		{name: "text", events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "partial"}}},
		{name: "thinking", events: []llm.StreamEvent{{Type: llm.SEThinkingDelta, Text: "partial"}}},
		{name: "tool_start", events: []llm.StreamEvent{{Type: llm.SEToolUseStart, ToolID: "probe-1", ToolName: "record_probe"}}},
		{name: "partial_tool_input", events: []llm.StreamEvent{
			{Type: llm.SEToolUseStart, ToolID: "probe-1", ToolName: "record_probe"},
			{Type: llm.SEToolInputJSON, Text: `{"partial":`},
		}},
		{name: "unknown_event", events: []llm.StreamEvent{{Type: llm.StreamEventType("future_adapter_event")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &scriptedLLMProvider{events: tc.events, err: io.ErrUnexpectedEOF}
			hooks := taskLLMStreamHooks{
				current: func() (taskLLMSelection, error) {
					return taskLLMSelection{
						profileID: 11, provider: provider,
						retry: agent.RetryConfig{StreamAttempts: 2, StreamInterval: time.Nanosecond},
					}, nil
				},
				exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
					t.Fatal("ordinary EOF must not advance the task profile cursor")
					return db.TaskLLMTransition{}, nil
				},
			}
			events, err := collectTaskLLMStream(streamTaskLLM(t.Context(), "7", llm.CompletionRequest{}, hooks))
			if !errors.Is(err, io.ErrUnexpectedEOF) || retryableWorkerModelError(harness.ReasonModelError, err) {
				t.Fatalf("committed error must preserve EOF and prevent worker replay: %T %v", err, err)
			}
			if provider.calls != 1 || !reflect.DeepEqual(events, tc.events) {
				t.Fatalf("calls=%d events=%+v, want 1 call with original committed events %+v", provider.calls, events, tc.events)
			}
		})
	}
}

func TestTaskLLMStreamCancellationKeepsCauseWithoutRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		events []llm.StreamEvent
	}{
		{name: "before_commit"},
		{name: "after_commit", events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "partial"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			provider := &scriptedLLMProvider{events: tc.events, err: context.Canceled}
			hooks := taskLLMStreamHooks{
				current: func() (taskLLMSelection, error) {
					return taskLLMSelection{profileID: 11, provider: provider}, nil
				},
				exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
					t.Fatal("cancellation must not advance the task profile cursor")
					return db.TaskLLMTransition{}, nil
				},
			}
			_, err := collectTaskLLMStream(streamTaskLLM(ctx, "7", llm.CompletionRequest{}, hooks))
			if !errors.Is(err, context.Canceled) || provider.calls != 1 {
				t.Fatalf("cancelled stream calls=%d error=%v, want 1 call preserving context.Canceled", provider.calls, err)
			}
			if isTaskLLMChainExhausted(err) || retryableWorkerModelError(harness.ReasonAbortedStreaming, err) {
				t.Fatalf("cancellation misclassified: %T %v", err, err)
			}
		})
	}
}
