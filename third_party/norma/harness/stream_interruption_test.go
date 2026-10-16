package harness

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

func TestInterruptedInputNeverDispatches(t *testing.T) {
	for _, input := range []string{"", `{"value":"partial`, `{"value":"complete but unclosed block"}`} {
		for _, mode := range []string{"provider_error", "cancel", "silent_cancel", "late_event"} {
			t.Run(mode+"/"+input, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var calls atomic.Int32
				ran := make(chan struct{}, 1)
				probe := tool.Build(tool.Spec{Name: "probe", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
					calls.Add(1)
					ran <- struct{}{}
					return tool.Text("unexpected"), nil
				}})
				failure := errors.New("synthetic stream interruption")
				model := func(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
					return func(y func(llm.StreamEvent, error) bool) {
						for _, e := range []llm.StreamEvent{{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 17}}, {Type: llm.SEToolUseStart, ToolID: "pending", ToolName: "probe"}, {Type: llm.SEToolInputJSON, Text: input}} {
							if !y(e, nil) {
								return
							}
						}
						if mode == "provider_error" {
							y(llm.StreamEvent{}, failure)
							return
						}
						cancel()
						if mode == "cancel" {
							y(llm.StreamEvent{}, ctx.Err())
						}
						if mode == "late_event" {
							y(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
						}
					}
				}
				var term *Terminal
				uses := 0
				for ev, err := range Query(ctx, QueryInput{Tools: tool.NewRegistry(probe), PermissionMode: permission.ModeBypass}, QueryDeps{CallModel: model}) {
					if err != nil {
						t.Fatal(err)
					}
					if ev.ToolUse != nil {
						uses++
					}
					if ev.Terminal != nil {
						term = ev.Terminal
					}
				}
				want := ReasonAbortedStreaming
				if mode == "provider_error" {
					want = ReasonModelError
				}
				if term == nil || term.Reason != want || term.Err == nil || term.Usage.InputTokens != 17 {
					t.Fatalf("terminal=%+v", term)
				}
				if uses != 0 {
					t.Errorf("unfinished call dispatched %d times", uses)
				}
				if mode == "provider_error" && uses > 0 {
					select {
					case <-ran:
					case <-time.After(time.Second):
					}
				}
				if uses == 0 && (term.InterruptedToolInput == nil || string(term.InterruptedToolInput.Input) != input) {
					t.Errorf("lost diagnostic input: %+v", term.InterruptedToolInput)
				}
				if calls.Load() != 0 {
					t.Errorf("unfinished call actually executed %d times", calls.Load())
				}
				for _, m := range term.Messages {
					if len(m.ToolUses()) > 0 {
						t.Fatal("unsubmitted call leaked into replayable history")
					}
				}
			})
		}
	}
}

type interruptionRecorder struct {
	messages []llm.Message
	usage    []llm.Usage
}

func (r *interruptionRecorder) RecordMessage(m llm.Message, u llm.Usage) {
	r.messages = append(r.messages, m)
	r.usage = append(r.usage, u)
}
func (*interruptionRecorder) RecordBoundary(llm.BoundaryMeta) {}

type interruptionCompactor struct{ reactive int }

func (*interruptionCompactor) Pre(_ context.Context, m []llm.Message, _ int) []llm.Message { return m }
func (c *interruptionCompactor) Reactive(_ context.Context, m []llm.Message) ([]llm.Message, bool) {
	c.reactive++
	return m, true
}
func (*interruptionCompactor) IsOverflow(error) bool { return true }

func TestStreamFailurePreservesActualAndUnknownOutcomes(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider_error", true: "cancel"}[cancelParent], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			var invocations atomic.Int32
			started, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			probe := tool.Build(tool.Spec{Name: "probe", Schema: map[string]any{"type": "object"}, Run: func(ctx context.Context, _ json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
				invocations.Add(1)
				if tc.ToolUseID == "actual" {
					r := tool.Text("actual success")
					r.Extra = []llm.Message{llm.UserText("standing guidance")}
					return r, nil
				}
				if tc.ToolUseID != "running" {
					t.Errorf("queued or pending call invoked: %q", tc.ToolUseID)
					return tool.Text("unexpected"), nil
				}
				close(started)
				<-ctx.Done()
				<-release
				defer close(stopped)
				// Deliberately ignore cancellation in the result to test late-write protection.
				return tool.Text("late success must not replace unknown receipt"), nil
			}})
			failure := errors.New("synthetic overflow-like stream error")
			modelCalls := 0
			model := func(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
				return func(y func(llm.StreamEvent, error) bool) {
					modelCalls++
					events := []llm.StreamEvent{{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 17, OutputTokens: 9, CacheReadTokens: 2}},
						{Type: llm.SEToolUseStart, ToolID: "actual", ToolName: "probe"}, {Type: llm.SEToolInputJSON, Text: `{}`},
						{Type: llm.SEToolUseStart, ToolID: "running", ToolName: "probe"}, {Type: llm.SEToolInputJSON, Text: `{}`},
						{Type: llm.SEToolUseStart, ToolID: "queued", ToolName: "probe"}, {Type: llm.SEToolInputJSON, Text: `{}`},
						{Type: llm.SEToolUseStart, ToolID: "pending", ToolName: "probe"}, {Type: llm.SEToolInputJSON, Text: `{"partial`}}
					for _, e := range events {
						if !y(e, nil) {
							return
						}
					}
					select {
					case <-started:
					case <-ctx.Done():
						t.Error("tool did not start")
					}
					if cancelParent {
						cancel()
						y(llm.StreamEvent{}, ctx.Err())
					} else {
						y(llm.StreamEvent{}, failure)
					}
				}
			}
			recorder := &interruptionRecorder{}
			compactor := &interruptionCompactor{}
			var term *Terminal
			var results []llm.ContentBlock
			var dispositions []ToolResultDisposition
			uses := 0
			for ev, err := range Query(ctx, QueryInput{Tools: tool.NewRegistry(probe), PermissionMode: permission.ModeBypass, Recorder: recorder, Compactor: compactor, TokenBudget: 100, MaxTurns: 1, Settlement: &Settlement{Prompt: "wrap up"}}, QueryDeps{CallModel: model}) {
				if err != nil {
					t.Fatal(err)
				}
				if ev.ToolUse != nil {
					uses++
					if ev.ToolUse.ID == "pending" {
						t.Fatal("dispatched unfinished call")
					}
				}
				if ev.ToolResult != nil {
					results = append(results, *ev.ToolResult)
					dispositions = append(dispositions, ev.ToolResultDisposition)
				}
				if ev.Terminal != nil {
					term = ev.Terminal
				}
			}
			close(release)
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("tool context not canceled")
			}
			wantReason := ReasonModelError
			if cancelParent {
				wantReason = ReasonAbortedStreaming
			}
			if term == nil || term.Reason != wantReason || modelCalls != 1 || compactor.reactive != 0 || invocations.Load() != 2 || uses != 3 || len(results) != 3 {
				t.Fatalf("terminal=%+v calls=%d reactive=%d invocations=%d uses=%d results=%+v", term, modelCalls, compactor.reactive, invocations.Load(), uses, results)
			}
			if term.Usage != (llm.Usage{InputTokens: 17, OutputTokens: 9, CacheReadTokens: 2}) || term.Turns != 1 {
				t.Fatalf("usage/turns=%+v", term)
			}
			if !reflect.DeepEqual(dispositions, []ToolResultDisposition{ToolResultActual, ToolResultInterrupted, ToolResultNotInvoked}) {
				t.Fatalf("receipt metadata=%+v", dispositions)
			}
			if results[0].IsError || results[0].Content[0].Text != "actual success" {
				t.Fatalf("real outcome lost: %+v", results[0])
			}
			if !results[1].IsError || !strings.Contains(results[1].Content[0].Text, "outcome unknown") {
				t.Fatalf("invented definitive running outcome: %+v", results[1])
			}
			if !results[2].IsError || !strings.Contains(results[2].Content[0].Text, "was not invoked") {
				t.Fatalf("queued outcome not distinguished: %+v", results[2])
			}
			if len(term.Messages) != 3 || len(term.Messages[0].ToolUses()) != 3 || len(term.Messages[1].Content) != 3 || term.Messages[2].Text() != "standing guidance" {
				t.Fatalf("lost tool exchange/extra: %+v", term.Messages)
			}
			if !reflect.DeepEqual(recorder.messages, term.Messages) || recorder.usage[0] != term.Usage {
				t.Fatalf("recorder mismatch: %+v", recorder)
			}
			projected := llm.MessagesForAPI(term.Messages)
			if len(projected) != 3 || len(projected[0].ToolUses()) != 3 {
				t.Fatalf("R17 pairing dropped valid exchange: %+v", projected)
			}
			if term.InterruptedToolInput == nil || string(term.InterruptedToolInput.Input) != `{"partial` {
				t.Fatalf("missing diagnostic input: %+v", term)
			}
			// Receipt snapshots cannot change when a cancellation-ignoring tool returns.
			if !reflect.DeepEqual(results, term.Messages[1].Content) {
				t.Fatal("terminal result differs from emitted receipt")
			}
		})
	}
}

func TestCompletedEmptyInputStillExecutesOnce(t *testing.T) {
	var calls atomic.Int32
	probe := tool.Build(tool.Spec{Name: "probe", Schema: map[string]any{"type": "object"}, Run: func(_ context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
		calls.Add(1)
		if string(in) != "{}" {
			t.Errorf("input=%s", in)
		}
		return tool.Text("ok"), nil
	}})
	turns := 0
	model := func(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
		return func(y func(llm.StreamEvent, error) bool) {
			turns++
			events := []llm.StreamEvent{{Type: llm.SETextDelta, Text: "done"}, {Type: llm.SEMessageDelta, StopReason: "end_turn", Usage: llm.Usage{OutputTokens: 10}}}
			if turns == 1 {
				events = []llm.StreamEvent{{Type: llm.SEToolUseStart, ToolID: "valid", ToolName: "probe"}, {Type: llm.SEMessageDelta, StopReason: "tool_use", Usage: llm.Usage{OutputTokens: 10}}, {Type: llm.SEMessageStop}}
			} else if len(req.Messages) != 2 || len(req.Messages[0].ToolUses()) != 1 || req.Messages[1].Content[0].ToolUseID != "valid" {
				t.Fatalf("next-turn pairing=%+v", req.Messages)
			}
			for _, e := range events {
				if !y(e, nil) {
					return
				}
			}
		}
	}
	var term *Terminal
	for ev, err := range Query(t.Context(), QueryInput{Tools: tool.NewRegistry(probe), PermissionMode: permission.ModeBypass, TokenBudget: 20}, QueryDeps{CallModel: model}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Terminal != nil {
			term = ev.Terminal
		}
	}
	if term == nil || term.Reason != ReasonCompleted || term.Text != "done" || term.Usage.OutputTokens != 20 || term.Turns != 2 || term.InterruptedToolInput != nil || calls.Load() != 1 || turns != 2 {
		t.Fatalf("normal completion changed: %+v calls=%d turns=%d", term, calls.Load(), turns)
	}
}

func TestStreamFailureWithoutDispatchedToolsCanRecover(t *testing.T) {
	c := &interruptionCompactor{}
	calls := 0
	model := func(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
		return func(y func(llm.StreamEvent, error) bool) {
			calls++
			if calls == 1 {
				y(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: "pending", ToolName: "probe"}, nil)
				y(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: `{"partial`}, nil)
				y(llm.StreamEvent{}, errors.New("synthetic overflow"))
				return
			}
			for _, m := range req.Messages {
				if len(m.ToolUses()) > 0 {
					t.Fatal("unfinished tool replayed")
				}
			}
			y(llm.StreamEvent{Type: llm.SETextDelta, Text: "recovered"}, nil)
		}
	}
	var term *Terminal
	for ev, err := range Query(t.Context(), QueryInput{Compactor: c}, QueryDeps{CallModel: model}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Terminal != nil {
			term = ev.Terminal
		}
	}
	if term == nil || term.Reason != ReasonCompleted || term.Text != "recovered" || term.InterruptedToolInput != nil || calls != 2 || c.reactive != 1 {
		t.Fatalf("recovery changed: %+v calls=%d reactive=%d", term, calls, c.reactive)
	}
}

func TestInterruptedDuplicateIDKeepsEarlierCall(t *testing.T) {
	probe := tool.Build(tool.Spec{Name: "probe", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
		return tool.Text("ok"), nil
	}})
	model := func(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
		return func(y func(llm.StreamEvent, error) bool) {
			for _, e := range []llm.StreamEvent{{Type: llm.SEToolUseStart, ToolID: "same", ToolName: "probe"}, {Type: llm.SEToolInputJSON, Text: `{}`}, {Type: llm.SEToolUseStart, ToolID: "same", ToolName: "probe"}, {Type: llm.SEToolInputJSON, Text: `{"partial`}} {
				if !y(e, nil) {
					return
				}
			}
			y(llm.StreamEvent{}, errors.New("synthetic interruption"))
		}
	}
	var term *Terminal
	for ev, err := range Query(t.Context(), QueryInput{Tools: tool.NewRegistry(probe), PermissionMode: permission.ModeBypass}, QueryDeps{CallModel: model}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Terminal != nil {
			term = ev.Terminal
		}
	}
	if term == nil || len(term.Messages) != 2 || len(term.Messages[0].ToolUses()) != 1 || len(term.Messages[1].Content) != 1 || len(llm.MessagesForAPI(term.Messages)[0].ToolUses()) != 1 {
		t.Fatalf("earlier completed block lost with duplicate ID: %+v", term)
	}
}

func TestStreamFailureCancelsWaitingApproval(t *testing.T) {
	entered, stopped := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	probe := tool.Build(tool.Spec{Name: "probe", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
		calls.Add(1)
		return tool.Text("unexpected"), nil
	}})
	approval := func(ctx context.Context, _ string, _ json.RawMessage, _ permission.Context) permission.Decision {
		close(entered)
		<-ctx.Done()
		defer close(stopped)
		return permission.Allowed()
	}
	model := func(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
		return func(y func(llm.StreamEvent, error) bool) {
			for _, e := range []llm.StreamEvent{{Type: llm.SEToolUseStart, ToolID: "approval", ToolName: "probe"}, {Type: llm.SEToolInputJSON, Text: `{}`}, {Type: llm.SETextDelta, Text: "waiting"}} {
				if !y(e, nil) {
					return
				}
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Error("approval did not start")
			}
			y(llm.StreamEvent{}, errors.New("synthetic error"))
		}
	}
	for _, err := range Query(t.Context(), QueryInput{Tools: tool.NewRegistry(probe), CanUseTool: approval}, QueryDeps{CallModel: model}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("approval retained live parent context after stream failure")
	}
	if calls.Load() != 0 {
		t.Fatal("approval allowed invocation after stream failure")
	}
}

func TestInterruptedReceiptCannotBeOverwritten(t *testing.T) {
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	probe := tool.Build(tool.Spec{Name: "probe", Schema: map[string]any{"type": "object"}, Run: func(ctx context.Context, _ json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
		close(started)
		<-ctx.Done()
		<-release
		tc.Emit(tool.ProgressInfo{Message: "late progress"})
		r := tool.Text("late success")
		r.Extra = []llm.Message{llm.UserText("late extra")}
		return r, nil
	}})
	l := &loop{ctx: t.Context(), toolCtx: t.Context(), in: QueryInput{Tools: tool.NewRegistry(probe), PermissionMode: permission.ModeBypass}, sem: make(chan struct{}, 1), yield: func(Event, error) bool { return true }}
	e := newStreamExec(l)
	defer e.cancel()
	trackedCall := &tracked{block: llm.ContentBlock{Type: llm.BlockToolUse, ID: "running", Name: "probe", Input: []byte(`{}`)}, status: stExecuting}
	e.tools = []*tracked{trackedCall}
	go func() { e.run(trackedCall); close(finished) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	e.cancel()
	if !e.drainSynthetic("outcome unknown") {
		t.Fatal("unexpected consumer stop")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("tool did not return")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if trackedCall.status != stYielded || trackedCall.disposition != ToolResultInterrupted || !trackedCall.result.IsError || trackedCall.result.Content[0].Text != "outcome unknown" || len(trackedCall.prog) != 0 || len(trackedCall.extra) != 0 {
		t.Fatalf("late return overwrote terminal receipt: %+v", trackedCall)
	}
}
