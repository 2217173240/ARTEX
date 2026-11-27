package server

import (
	"context"
	"iter"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

type captureReqProvider struct {
	got    llm.CompletionRequest
	events []llm.StreamEvent
}

func (p *captureReqProvider) Stream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.got = req
	return func(yield func(llm.StreamEvent, error) bool) {
		for _, ev := range p.events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func (p *captureReqProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return accumulateStreamForTest(ctx, p.Stream, req)
}

func TestStreamCollectTextLeavesThinkingUnset(t *testing.T) {
	t.Parallel()
	prov := &captureReqProvider{
		events: []llm.StreamEvent{
			{Type: llm.SEThinkingDelta, Text: "reasoning"},
			{Type: llm.SETextDelta, Text: `{"action":"allow"}`},
		},
	}

	got, err := streamCollectText(context.Background(), prov, "judge system", "review input")
	if err != nil {
		t.Fatalf("streamCollectText error = %v", err)
	}
	if got != `{"action":"allow"}` {
		t.Fatalf("got text = %q, want JSON verdict only", got)
	}
	if prov.got.Thinking != "" {
		t.Fatalf("CompletionRequest.Thinking = %q, want empty (unset)", prov.got.Thinking)
	}
	if prov.got.MaxTokens != 1024 {
		t.Fatalf("CompletionRequest.MaxTokens = %d, want 1024", prov.got.MaxTokens)
	}
	if prov.got.Temperature == nil || *prov.got.Temperature != 0 {
		t.Fatalf("CompletionRequest.Temperature = %v, want 0", prov.got.Temperature)
	}
}
