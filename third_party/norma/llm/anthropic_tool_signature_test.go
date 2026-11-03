package llm

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAnthropicBodyStripsToolSignaturesWithoutMutatingHistory(t *testing.T) {
	for _, thinking := range []string{"enabled", "disabled"} {
		t.Run(thinking, func(t *testing.T) {
			messages := []Message{
				{Role: RoleAssistant, Content: []ContentBlock{
					{Type: BlockThinking, Thinking: "reasoning", Signature: "anthropic-signature"},
					{Type: BlockToolUse, ID: "call-1", Name: "Read", Input: json.RawMessage(`{"path":"/a"}`), ThoughtSignature: "gemini-signature"},
				}},
				{Role: RoleUser, Content: []ContentBlock{{Type: BlockToolResult, ToolUseID: "call-1", Content: []ContentBlock{{Type: BlockText, Text: "result", ThoughtSignature: "nested-signature"}}}}},
			}
			before, err := json.Marshal(messages)
			if err != nil {
				t.Fatal(err)
			}
			provider := anthropicProvider{cfg: Config{Model: "claude", ThinkingType: thinking}}
			for _, stream := range []bool{false, true} {
				body, err := provider.buildBody(CompletionRequest{Messages: messages, Thinking: thinking}, stream)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(body), "thought_signature") {
					t.Fatalf("foreign signature reached Anthropic: %s", body)
				}
				var decoded anthropicReq
				if err := json.Unmarshal(body, &decoded); err != nil {
					t.Fatal(err)
				}
				blocks := decoded.Messages[0].Content
				if thinking == "enabled" && (blocks[0].Type != BlockThinking || blocks[0].Signature != "anthropic-signature") {
					t.Fatalf("thinking signature lost: %+v", blocks)
				}
				tool := blocks[len(blocks)-1]
				if tool.ID != "call-1" || string(tool.Input) != `{"path":"/a"}` {
					t.Fatalf("tool changed: %+v", tool)
				}
			}
			after, err := json.Marshal(messages)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("history mutated: %s", after)
			}
		})
	}
}
