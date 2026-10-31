package agent

import (
	"testing"

	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
)

func TestCompressionResultBody(t *testing.T) {
	for _, stopReason := range []string{"max_tokens", "end_turn", "stop_sequence", "tool_use", "pause_turn", "refusal", "", "future_reason"} {
		for _, text := range []string{"  complete body\n", " \t\n", ""} {
			t.Run(stopReason+"/"+text, func(t *testing.T) {
				msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(text)}}
				body, err := compressionResultBody(msg, stopReason)
				wantBody, wantError := "complete body", ""
				if stopReason == "max_tokens" {
					wantBody, wantError = "", "incomplete compaction body from model"
				} else if text != "  complete body\n" {
					wantBody, wantError = "", "empty body from model"
				}
				if body != wantBody {
					t.Fatalf("body = %q, want %q", body, wantBody)
				}
				if wantError == "" {
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
					return
				}
				if err == nil || err.Error() != wantError {
					t.Fatalf("error = %v, want %q", err, wantError)
				}
				if compaction.IsOverflow(err) || IsQuotaExhaustedMessage(err.Error()) {
					t.Fatalf("result rejection activated provider failure policy: %v", err)
				}
			})
		}
	}
}

func TestCompressionResultBodyTextBlocks(t *testing.T) {
	msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: "private reasoning"},
		llm.TextBlock("  first"),
		llm.TextBlock(" second\n"),
	}}
	body, err := compressionResultBody(msg, "end_turn")
	if err != nil || body != "first second" {
		t.Fatalf("body = %q, error = %v", body, err)
	}
}
