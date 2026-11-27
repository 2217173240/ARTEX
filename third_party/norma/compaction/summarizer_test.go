package compaction

import (
	"context"
	"iter"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

type captureProvider struct {
	reqs []llm.CompletionRequest
}

func (p *captureProvider) Stream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.reqs = append(p.reqs, req)
	return func(yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "<summary>ok</summary>"}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}

func (p *captureProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.reqs = append(p.reqs, req)
	return llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentBlock{llm.TextBlock("<summary>ok</summary>")},
	}, "end_turn", llm.Usage{}, nil
}

func TestProviderSummarizerDoesNotDisableThinking(t *testing.T) {
	for _, nonStreaming := range []bool{false, true} {
		prov := &captureProvider{}
		sum := ProviderSummarizer(prov, 4096, nonStreaming)
		got, err := sum(context.Background(), []llm.Message{llm.UserText("hello")})
		if err != nil {
			t.Fatalf("nonStreaming=%v summarize error: %v", nonStreaming, err)
		}
		if got != "Summary:\nok" {
			t.Fatalf("nonStreaming=%v got=%q, want %q", nonStreaming, got, "Summary:\nok")
		}
		if len(prov.reqs) != 1 {
			t.Fatalf("nonStreaming=%v calls=%d, want 1", nonStreaming, len(prov.reqs))
		}
		if prov.reqs[0].Thinking != "" {
			t.Fatalf("nonStreaming=%v Thinking=%q, want empty", nonStreaming, prov.reqs[0].Thinking)
		}
	}
}
