package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

func TestCaptureRunEmptyOutputIsModelError(t *testing.T) {
	for _, nonStreaming := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			events []llm.StreamEvent
		}{
			{name: "empty"},
			{name: "whitespace", events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: " \n\t"}}},
			{name: "thinking_only", events: []llm.StreamEvent{{Type: llm.SEThinkingDelta, Text: "internal reasoning"}}},
			{name: "thinking_and_whitespace", events: []llm.StreamEvent{
				{Type: llm.SEThinkingDelta, Text: "internal reasoning"}, {Type: llm.SETextDelta, Text: " \n\t"},
			}},
		} {
			t.Run(tc.name+map[bool]string{false: "/streaming", true: "/non_streaming"}[nonStreaming], func(t *testing.T) {
				events := append([]llm.StreamEvent{{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 11, CacheReadTokens: 3}}}, tc.events...)
				events = append(events, llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn", Usage: llm.Usage{OutputTokens: 7, CacheWriteTokens: 2}}, llm.StreamEvent{Type: llm.SEMessageStop})
				s := agentcore.NewSession(agentcore.Options{Provider: captureOutputProvider(events), NonStreaming: nonStreaming, MaxTurns: 1, WorkingDir: t.TempDir()})
				defer s.Close()
				var activities []db.Activity
				_, reason, err := captureRunSession(t.Context(), s, "empty completion fixture", func(a db.Activity) { activities = append(activities, a) })
				if reason != harness.ReasonModelError || err == nil {
					t.Fatalf("reason=%q error=%v, want model_error and an error", reason, err)
				}
				result := captureOutputResult(t, activities)
				if !result.IsError || !strings.Contains(result.Detail, string(harness.ReasonModelError)) || !strings.Contains(result.Detail, err.Error()) {
					t.Fatalf("result does not explain empty model failure: %+v", result)
				}
				assertCapturedResultUsage(t, activities, 11, 7, 3, 2)
				if got := s.Usage(); got != (llm.Usage{InputTokens: 11, OutputTokens: 7, CacheReadTokens: 3, CacheWriteTokens: 2}) {
					t.Fatalf("session usage=%+v", got)
				}
				// Classification belongs to the host: the SDK's assistant message
				// stays intact, including its original thinking/whitespace blocks.
				acc := llm.NewAccumulator()
				for _, ev := range events {
					acc.Add(ev)
				}
				messages := s.Messages()
				if !reflect.DeepEqual(messages[len(messages)-1], acc.Message()) {
					t.Fatalf("classification changed SDK message: %+v", messages[len(messages)-1])
				}
			})
		}
	}
}

func TestCaptureRunEmptyStreamIsModelError(t *testing.T) {
	var activities []db.Activity
	_, reason, err := captureRun(t.Context(), agentcore.Options{Provider: captureOutputProvider(nil), MaxTurns: 1, WorkingDir: t.TempDir()}, "empty stream", func(a db.Activity) { activities = append(activities, a) })
	if reason != harness.ReasonModelError || err == nil || !captureOutputResult(t, activities).IsError {
		t.Fatalf("reason=%q error=%v activities=%+v", reason, err, activities)
	}
	assertCapturedResultUsage(t, activities, 0, 0, 0, 0)
}

func TestCaptureRunEmptyOutputDoesNotReusePriorOutput(t *testing.T) {
	for _, priorTool := range []bool{false, true} {
		t.Run(map[bool]string{false: "prior_text", true: "prior_tool"}[priorTool], func(t *testing.T) {
			calls, executions := 0, 0
			provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
				calls++
				var events []llm.StreamEvent
				if calls == 1 {
					if priorTool {
						events = captureOutputToolEvents()
					} else {
						events = []llm.StreamEvent{{Type: llm.SETextDelta, Text: "previous response"}}
					}
				}
				for _, ev := range events {
					if !yield(ev, nil) {
						return
					}
				}
			}}
			s := agentcore.NewSession(agentcore.Options{Provider: provider, Tools: []tool.CoreTool{captureOutputProbe(&executions)}, PermissionMode: permission.ModeBypass, MaxTurns: 2, WorkingDir: t.TempDir()})
			defer s.Close()
			if _, reason, err := captureRunSession(t.Context(), s, "first prompt", nil); reason != harness.ReasonCompleted || err != nil {
				t.Fatalf("first prompt reason=%q error=%v", reason, err)
			}
			prior, err := json.Marshal(s.Messages())
			if err != nil {
				t.Fatal(err)
			}
			priorCount := len(s.Messages())
			var activities []db.Activity
			_, reason, err := captureRunSession(t.Context(), s, "second prompt", func(a db.Activity) { activities = append(activities, a) })
			if reason != harness.ReasonModelError || err == nil || !captureOutputResult(t, activities).IsError {
				t.Fatalf("second prompt reason=%q error=%v activities=%+v", reason, err, activities)
			}
			after, err := json.Marshal(s.Messages()[:priorCount])
			if err != nil || string(after) != string(prior) {
				t.Fatalf("prior history changed: error=%v before=%s after=%s", err, prior, after)
			}
		})
	}
}

func TestCaptureRunToolOnlyOutputIsCompleted(t *testing.T) {
	executions, calls := 0, 0
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		calls++
		events := []llm.StreamEvent{{Type: llm.SEMessageDelta, StopReason: "end_turn"}}
		if calls == 1 {
			events = captureOutputToolEvents()
		}
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}}
	var activities []db.Activity
	text, reason, err := captureRun(t.Context(), agentcore.Options{Provider: provider, Tools: []tool.CoreTool{captureOutputProbe(&executions)}, PermissionMode: permission.ModeBypass, MaxTurns: 2, WorkingDir: t.TempDir()}, "tool-only completion", func(a db.Activity) { activities = append(activities, a) })
	if text != "" || reason != harness.ReasonCompleted || err != nil || executions != 1 || captureOutputResult(t, activities).IsError {
		t.Fatalf("text=%q reason=%q error=%v executions=%d activities=%+v", text, reason, err, executions, activities)
	}
}

func TestCaptureRunUsableTextRetainsTerminalDedup(t *testing.T) {
	provider := captureOutputProvider([]llm.StreamEvent{
		{Type: llm.SEThinkingDelta, Text: "internal reasoning"},
		{Type: llm.SETextDelta, Text: "do"}, {Type: llm.SETextDelta, Text: "ne"},
		{Type: llm.SEMessageDelta, StopReason: "end_turn", Usage: llm.Usage{InputTokens: 11, OutputTokens: 7, CacheReadTokens: 3, CacheWriteTokens: 2}},
	})
	var activities []db.Activity
	text, reason, err := captureRun(t.Context(), agentcore.Options{Provider: provider, MaxTurns: 1, WorkingDir: t.TempDir()}, "normal completion", func(a db.Activity) { activities = append(activities, a) })
	if text != "done" || reason != harness.ReasonCompleted || err != nil {
		t.Fatalf("text=%q reason=%q error=%v", text, reason, err)
	}
	result := captureOutputResult(t, activities)
	if result.IsError || result.Detail != "done" {
		t.Fatalf("unexpected result: %+v", result)
	}
	thinking := 0
	for _, a := range activities {
		if a.Kind == "text" {
			t.Fatalf("final text recorded twice: %+v", activities)
		}
		if a.Kind == "thinking" {
			thinking++
		}
	}
	if thinking != 1 {
		t.Fatalf("thinking records=%d, want 1", thinking)
	}
	assertCapturedResultUsage(t, activities, 11, 7, 3, 2)
}

func TestCaptureRunEmptyOutputPreservesProviderError(t *testing.T) {
	wantErr := errors.New("fixture provider failure")
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 11, CacheReadTokens: 3}}, nil) {
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageDelta, Usage: llm.Usage{OutputTokens: 7, CacheWriteTokens: 2}}, nil) {
			return
		}
		yield(llm.StreamEvent{}, wantErr)
	}}
	var activities []db.Activity
	_, reason, err := captureRun(t.Context(), agentcore.Options{Provider: provider, MaxTurns: 1, WorkingDir: t.TempDir()}, "provider failure", func(a db.Activity) { activities = append(activities, a) })
	if reason != harness.ReasonModelError || !errors.Is(err, wantErr) || !captureOutputResult(t, activities).IsError {
		t.Fatalf("reason=%q error=%v activities=%+v", reason, err, activities)
	}
	assertCapturedResultUsage(t, activities, 11, 7, 3, 2)
}

func TestCaptureRunEmptyOutputPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.SEThinkingDelta, Text: "internal reasoning"}, nil) {
			return
		}
		cancel()
		yield(llm.StreamEvent{}, context.Canceled)
	}}
	var activities []db.Activity
	_, reason, err := captureRun(ctx, agentcore.Options{Provider: provider, MaxTurns: 1, WorkingDir: t.TempDir()}, "cancel fixture", func(a db.Activity) { activities = append(activities, a) })
	if reason != harness.ReasonAbortedStreaming || !errors.Is(err, context.Canceled) || !captureOutputResult(t, activities).IsError {
		t.Fatalf("reason=%q error=%v activities=%+v", reason, err, activities)
	}
}

func TestCaptureRunToolBudgetPreservesMaxTurns(t *testing.T) {
	executions := 0
	var activities []db.Activity
	_, reason, err := captureRun(t.Context(), agentcore.Options{Provider: captureOutputProvider(captureOutputToolEvents()), Tools: []tool.CoreTool{captureOutputProbe(&executions)}, PermissionMode: permission.ModeBypass, MaxTurns: 1, WorkingDir: t.TempDir()}, "one-turn budget", func(a db.Activity) { activities = append(activities, a) })
	if reason != harness.ReasonMaxTurns || err != nil || captureOutputResult(t, activities).IsError {
		t.Fatalf("reason=%q error=%v activities=%+v", reason, err, activities)
	}
}

func TestCaptureRunEmptyOutputPreservesStopHookReason(t *testing.T) {
	var activities []db.Activity
	_, reason, err := captureRun(t.Context(), agentcore.Options{Provider: captureOutputProvider(nil), Hooks: captureOutputStopHook{}, MaxTurns: 1, WorkingDir: t.TempDir()}, "stopped by hook", func(a db.Activity) { activities = append(activities, a) })
	if reason != harness.ReasonStopHookPrevented || err != nil || captureOutputResult(t, activities).IsError {
		t.Fatalf("reason=%q error=%v activities=%+v", reason, err, activities)
	}
}

func TestCaptureRunToolOutputPreservesAbortedTools(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	probe := tool.Build(tool.Spec{Name: "CaptureOutputProbe", Schema: map[string]any{"type": "object"},
		Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
			cancel()
			return tool.Result{}, context.Canceled
		},
	})
	var activities []db.Activity
	_, reason, err := captureRun(ctx, agentcore.Options{Provider: captureOutputProvider(captureOutputToolEvents()), Tools: []tool.CoreTool{probe}, PermissionMode: permission.ModeBypass, MaxTurns: 2, WorkingDir: t.TempDir()}, "cancel during tool", func(a db.Activity) { activities = append(activities, a) })
	if reason != harness.ReasonAbortedTools || !errors.Is(err, context.Canceled) || !captureOutputResult(t, activities).IsError {
		t.Fatalf("reason=%q error=%v activities=%+v", reason, err, activities)
	}
}

type captureOutputStopHook struct{}

func (captureOutputStopHook) PreToolUse(context.Context, string, []byte) (bool, string, []byte) {
	return false, "", nil
}

func (captureOutputStopHook) PostToolUse(context.Context, string, []byte, []byte, bool) {}

func (captureOutputStopHook) Stop(context.Context, []llm.Message) (bool, []string, string) {
	return true, nil, ""
}

func captureOutputProvider(events []llm.StreamEvent) captureUsageProvider {
	return captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}}
}

func captureOutputToolEvents() []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: llm.SEToolUseStart, ToolID: "output-probe-call", ToolName: "CaptureOutputProbe"},
		{Type: llm.SEToolInputJSON, Text: `{}`}, {Type: llm.SEMessageDelta, StopReason: "tool_use"},
	}
}

func captureOutputProbe(executions *int) tool.CoreTool {
	return tool.Build(tool.Spec{Name: "CaptureOutputProbe", Schema: map[string]any{"type": "object"},
		Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
			*executions++
			return tool.Text("fixed local fixture result"), nil
		},
	})
}

func captureOutputResult(t *testing.T, activities []db.Activity) db.Activity {
	t.Helper()
	var result db.Activity
	count := 0
	for _, a := range activities {
		if a.Kind == "result" {
			result, count = a, count+1
		}
	}
	if count != 1 {
		t.Fatalf("result count=%d, want 1: %+v", count, activities)
	}
	return result
}
