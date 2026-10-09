package llmpool

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
)

type markedCallTimeout struct{}

func (markedCallTimeout) Error() string        { return "call timed out" }
func (markedCallTimeout) Unwrap() error        { return context.DeadlineExceeded }
func (markedCallTimeout) LLMCallTimeout() bool { return true }

func TestCallTimeoutFailoverClassification(t *testing.T) {
	live := context.Background()
	if !shouldFailover(live, fmt.Errorf("wrapped: %w", markedCallTimeout{})) {
		t.Fatal("marked child timeout should fail over")
	}
	if shouldFailover(live, context.DeadlineExceeded) || shouldFailover(live, context.Canceled) {
		t.Fatal("ordinary context errors should not fail over")
	}
	canceled, cancel := context.WithCancel(live)
	cancel()
	if shouldFailover(canceled, markedCallTimeout{}) {
		t.Fatal("parent cancellation must win")
	}
	expired, done := context.WithDeadline(live, time.Now().Add(-time.Second))
	defer done()
	if shouldFailover(expired, markedCallTimeout{}) {
		t.Fatal("parent deadline must win")
	}
}

func TestCallTimeoutPreservesStreamCommitBoundary(t *testing.T) {
	for _, commitEvent := range []llm.StreamEvent{{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: "partial"}, {Type: llm.SEThinkingDelta, Text: "partial"}, {Type: llm.SEToolUseStart}, {Type: llm.SEToolInputJSON, Text: "partial"}} {
		committed := streamEventCommitsOutput(commitEvent)
		events := []llm.StreamEvent{{Type: llm.SEMessageStart}}
		if committed {
			events = append(events, commitEvent)
		}
		first := &fakeProv{events: [][]llm.StreamEvent{events}, errs: []error{markedCallTimeout{}}}
		second := &fakeProv{events: [][]llm.StreamEvent{{{Type: llm.SETextDelta, Text: "backup"}}}}
		pool := New([]*Member{{Name: "first", Prov: first}, {Name: "second", Prov: second}}, nil)
		var text string
		var got error
		for ev, err := range pool.Stream(context.Background(), llm.CompletionRequest{}) {
			text += ev.Text
			if err != nil {
				got = err
			}
		}
		if committed {
			if second.calls != 0 || text != commitEvent.Text || !errors.Is(got, context.DeadlineExceeded) {
				t.Fatalf("committed: %q %v calls=%d", text, got, second.calls)
			}
		} else {
			if second.calls != 1 || text != "backup" || got != nil {
				t.Fatalf("uncommitted: %q %v calls=%d", text, got, second.calls)
			}
		}
	}
}

func TestCallTimeoutCompleteFailover(t *testing.T) {
	first := &fakeProv{errs: []error{markedCallTimeout{}}}
	second := &fakeProv{}
	p := New([]*Member{{Name: "first", Prov: first}, {Name: "second", Prov: second}}, nil)
	if _, _, _, err := p.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatal(err)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("calls %d/%d", first.calls, second.calls)
	}
}
