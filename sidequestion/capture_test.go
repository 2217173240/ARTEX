package sidequestion

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestCloneRequestStructuralDeepIsolation(t *testing.T) {
	temp := 0.42
	nestedInput := json.RawMessage(`{"target":"https://example.com","flags":["a","b"]}`)
	orig := llm.CompletionRequest{
		System:          []string{"static prompt", "dynamic prompt"},
		DynamicBoundary: 1,
		MaxTokens:       4096,
		Temperature:     &temp,
		Stop:            []string{"STOP1", "STOP2"},
		Thinking:        "enabled",
		Messages: []llm.Message{
			{
				Role: llm.RoleUser,
				Content: []llm.ContentBlock{
					{Type: llm.BlockText, Text: "inspect target"},
				},
			},
			{
				Role: llm.RoleAssistant,
				Content: []llm.ContentBlock{
					{Type: llm.BlockThinking, Thinking: "plan", Signature: "sig-1"},
					{
						Type:             llm.BlockToolUse,
						ID:               "call-1",
						Name:             "http_get",
						Input:            nestedInput,
						ThoughtSignature: "tsig-1",
					},
				},
			},
			{
				Role: llm.RoleUser,
				Content: []llm.ContentBlock{
					{
						Type:      llm.BlockToolResult,
						ToolUseID: "call-1",
						IsError:   true,
						Content: []llm.ContentBlock{
							{
								Type:    llm.BlockToolResult,
								Content: []llm.ContentBlock{llm.TextBlock("nested result")},
							},
						},
					},
				},
			},
		},
		Tools: []llm.ToolSchema{
			{
				Name:        "http_get",
				Description: "fetch URL",
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"url"},
					"enum":     []any{"a", "b", map[string]any{"nested": []int{1, 2}}},
					"tags":     map[string]string{"env": "prod"},
					"raw":      json.RawMessage(`{"k":1}`),
					"properties": map[string]any{
						"url": map[string]any{
							"type": "string",
						},
					},
				},
			},
		},
	}

	cloned, err := CloneRequest(orig)
	if err != nil {
		t.Fatalf("CloneRequest failed: %v", err)
	}
	if !reflect.DeepEqual(orig, cloned) {
		t.Fatalf("cloned request differs from original:\norig:   %+v\ncloned: %+v", orig, cloned)
	}

	// Save an independent baseline JSON serialization of cloned before mutating orig.
	baselineBytes, err := json.Marshal(cloned)
	if err != nil {
		t.Fatal(err)
	}

	// Mutate every mutable structure in orig.
	orig.System[0] = "MUTATED_SYSTEM"
	orig.Stop[1] = "MUTATED_STOP"
	*orig.Temperature = 1.99
	orig.DynamicBoundary = 99
	orig.MaxTokens = 1
	orig.Thinking = "disabled"

	orig.Messages[0].Role = llm.RoleAssistant
	orig.Messages[0].Content[0].Text = "MUTATED_TEXT"
	orig.Messages[1].Content[0].Thinking = "MUTATED_THINKING"
	orig.Messages[1].Content[1].Input[2] = 'X'
	orig.Messages[2].Content[0].Content[0].Content[0].Text = "MUTATED_NESTED"
	orig.Messages[2].Content[0].IsError = false

	orig.Tools[0].Name = "mutated_tool"
	schema := orig.Tools[0].InputSchema
	schema["type"] = "array"
	schema["required"].([]string)[0] = "mutated"
	schema["enum"].([]any)[0] = "mutated_enum"
	schema["enum"].([]any)[2].(map[string]any)["nested"].([]int)[0] = 999
	schema["tags"].(map[string]string)["env"] = "dev"
	schema["raw"].(json.RawMessage)[2] = 'Z'
	schema["properties"].(map[string]any)["url"].(map[string]any)["type"] = "integer"

	afterBytes, err := json.Marshal(cloned)
	if err != nil {
		t.Fatal(err)
	}
	if string(baselineBytes) != string(afterBytes) {
		t.Fatalf("cloned request mutated when original changed:\nbefore: %s\nafter:  %s", baselineBytes, afterBytes)
	}
}

func TestCloneRequestPreservesNilAndEmptySlices(t *testing.T) {
	empty := llm.CompletionRequest{
		System:   []string{},
		Messages: []llm.Message{},
		Tools:    []llm.ToolSchema{},
		Stop:     nil,
	}
	cloned, err := CloneRequest(empty)
	if err != nil {
		t.Fatal(err)
	}
	if cloned.System == nil || len(cloned.System) != 0 {
		t.Fatalf("expected non-nil empty System, got %#v", cloned.System)
	}
	if cloned.Messages == nil || len(cloned.Messages) != 0 {
		t.Fatalf("expected non-nil empty Messages, got %#v", cloned.Messages)
	}
	if cloned.Tools == nil || len(cloned.Tools) != 0 {
		t.Fatalf("expected non-nil empty Tools, got %#v", cloned.Tools)
	}
	if cloned.Stop != nil {
		t.Fatalf("expected nil Stop, got %#v", cloned.Stop)
	}
	if cloned.Temperature != nil {
		t.Fatalf("expected nil Temperature, got %#v", cloned.Temperature)
	}
}
