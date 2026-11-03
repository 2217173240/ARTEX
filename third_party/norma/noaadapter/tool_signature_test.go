package noaadapter

import (
	"encoding/json"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestToolSignatureProjectReassemble(t *testing.T) {
	const original = `{"file_path":"/original.go"}`
	const signature = "opaque+/=\n工具签名"
	for _, tc := range []struct{ name, rewrite, wantInput, wantSignature string }{
		{"unchanged", original, original, signature},
		{"rewritten", `{"file_path":"/rewritten.go"}`, `{"file_path":"/rewritten.go"}`, ""},
		{"format-rewritten", `{ "file_path": "/original.go" }`, `{ "file_path": "/original.go" }`, ""},
		{"invalid-rewrite", `{"file_path":`, original, signature},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := toolUse("call-1", "Read", original)
			call.ThoughtSignature = signature
			messages := []llm.Message{assistantWith(call), toolResult("call-1", "contents")}
			// 使用实际历史记录的 JSON 编解码路径。
			raw, err := json.Marshal(messages)
			if err != nil {
				t.Fatal(err)
			}
			var restored []llm.Message
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			cores, sidecar := Project(restored)
			if sidecar.ToolSignatures[cores[0].ID] != signature {
				t.Fatal("sidecar lost tool signature")
			}
			cores[0].Text = tc.rewrite
			out := Reassemble(cores, restored, sidecar, ReassembleOptions{})
			if len(out) != 2 || len(out[0].Content) != 1 {
				t.Fatalf("unexpected rebuilt history: %+v", out)
			}
			got := out[0].Content[0]
			if string(got.Input) != tc.wantInput || got.ThoughtSignature != tc.wantSignature {
				t.Fatalf("rebuilt input/signature = %q / %q", got.Input, got.ThoughtSignature)
			}
			if restored[0].Content[0].ThoughtSignature != signature || string(restored[0].Content[0].Input) != original {
				t.Fatal("original history mutated")
			}
			if sidecar.ToolSignatures[cores[0].ID] != signature {
				t.Fatal("rebuild mutated sidecar")
			}
		})
	}
}
