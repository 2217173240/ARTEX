package llm

import "testing"

func TestAccumulatorIndexedToolsAndSignatures(t *testing.T) {
	zero, one := 0, 1
	a := NewAccumulator()
	for _, ev := range []StreamEvent{
		{Type: SEToolUseSignature, ToolIndex: &zero, Text: "sig-zero"},
		{Type: SEToolInputJSON, ToolIndex: &zero, Text: `{"zero":`},
		{Type: SEToolUseStart, ToolIndex: &one, ToolID: "one", ToolName: "first"},
		{Type: SEToolInputJSON, ToolIndex: &one, Text: `{"one":1}`},
		{Type: SEToolUseStart, ToolIndex: &zero, ToolID: "zero", ToolName: "second"},
		{Type: SEToolUseSignature, ToolIndex: &one, Text: "sig-one"},
		{Type: SEToolInputJSON, ToolIndex: &zero, Text: `0}`},
	} {
		a.Add(ev)
	}
	msg := a.Message()
	if len(msg.Content) != 2 {
		t.Fatalf("blocks: %+v", msg.Content)
	}
	for i, want := range []struct{ id, input, sig string }{
		{"one", `{"one":1}`, "sig-one"}, {"zero", `{"zero":0}`, "sig-zero"},
	} {
		got := msg.Content[i]
		if got.ID != want.id || string(got.Input) != want.input || got.ThoughtSignature != want.sig {
			t.Fatalf("block %d: %+v", i, got)
		}
	}
}

func TestAccumulatorLegacyToolSignatureAndFallback(t *testing.T) {
	a := NewAccumulator()
	zero := 0
	a.Add(StreamEvent{Type: SEToolUseSignature, ToolIndex: &zero, Text: "orphan"})
	if len(a.Message().Content) != 0 {
		t.Fatal("signature created a block")
	}
	a.Add(StreamEvent{Type: SEToolUseStart, ToolID: "legacy"})
	a.Add(StreamEvent{Type: SEToolInputJSON, Text: "invalid"})
	a.Add(StreamEvent{Type: SEToolUseSignature, Text: "legacy-signature"})
	b := a.Message().Content[0]
	if string(b.Input) != "{}" || b.ThoughtSignature != "legacy-signature" {
		t.Fatalf("legacy block: %+v", b)
	}
}
