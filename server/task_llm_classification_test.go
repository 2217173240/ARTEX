package server

import (
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestStreamEventCommitsOutput(t *testing.T) {
	index := 0
	cases := []struct {
		name  string
		event llm.StreamEvent
		want  bool
	}{
		{"early tool signature at index zero", llm.StreamEvent{Type: llm.SEToolUseSignature, Text: "opaque-signature", ToolIndex: &index}, false},
		{"thinking signature", llm.StreamEvent{Type: llm.SEThinkingSignature, Text: "opaque-signature"}, false},
		{"tool start", llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: "call-1", ToolName: "search", ToolIndex: &index}, true},
		{"text output", llm.StreamEvent{Type: llm.SETextDelta, Text: "answer"}, true},
		{"thinking output", llm.StreamEvent{Type: llm.SEThinkingDelta, Text: "reasoning"}, true},
		{"tool input", llm.StreamEvent{Type: llm.SEToolInputJSON, Text: "{}"}, true},
		{"empty text", llm.StreamEvent{Type: llm.SETextDelta}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := streamEventCommitsOutput(tc.event); got != tc.want {
				t.Fatalf("streamEventCommitsOutput(%+v) = %v, want %v", tc.event, got, tc.want)
			}
		})
	}
}
