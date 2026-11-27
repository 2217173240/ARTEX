package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func makeToolExchange(idx int, toolName, resultText string) []llm.Message {
	id := fmt.Sprintf("call_%d", idx)
	return []llm.Message{
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{{
				Type:  llm.BlockToolUse,
				ID:    id,
				Name:  toolName,
				Input: json.RawMessage(`{}`),
			}},
		},
		{
			Role: llm.RoleUser,
			Content: []llm.ContentBlock{{
				Type:      llm.BlockToolResult,
				ToolUseID: id,
				Content:   []llm.ContentBlock{llm.TextBlock(resultText)},
			}},
		},
	}
}

func toolResultText(m llm.Message) string {
	if len(m.Content) == 0 || len(m.Content[0].Content) == 0 {
		return ""
	}
	return m.Content[0].Content[0].Text
}

func TestMicroCompactExtraToolsAndPrefixes(t *testing.T) {
	payload := strings.Repeat("x", 400)
	var msgs []llm.Message
	tools := []string{
		"traffic_detail",
		"mcp__demo__scan",
		"add_intent", // non-compactable: must never be cleared
		"traffic_detail",
		"mcp__demo__fetch",
		"Read",
		"traffic_list",
		"mcp__other__info",
	}
	for i, name := range tools {
		msgs = append(msgs, makeToolExchange(i, name, fmt.Sprintf("%s-%d-%s", name, i, payload))...)
	}

	c := New(Config{
		ContextWindow:         1000,
		MaxOutputTokens:       100,
		ExtraCompactableTools: []string{"traffic_detail", "traffic_list"},
		CompactablePrefixes:   []string{"mcp__"},
	}, nil)

	out := c.Pre(context.Background(), msgs, 0)

	// Compactable tool order: 0(traffic_detail), 1(mcp__demo__scan), 3(traffic_detail),
	// 4(mcp__demo__fetch), 5(Read), 6(traffic_list), 7(mcp__other__info) -> 7 total.
	// microKeepRecent = 5, so 0 and 1 must be cleared; 3,4,5,6,7 kept; 2 (non-compactable) kept.
	for i := range tools {
		resMsg := out[i*2+1]
		got := toolResultText(resMsg)
		switch i {
		case 0, 1:
			if got != microClearedMessage {
				t.Fatalf("tool %d (%s) = %q, want cleared", i, tools[i], got)
			}
		default:
			if got == microClearedMessage {
				t.Fatalf("tool %d (%s) was unexpectedly cleared", i, tools[i])
			}
		}
	}

	// Default config (empty ExtraCompactableTools/CompactablePrefixes) must leave
	// host-specific tools and mcp__ calls untouched.
	plain := New(Config{ContextWindow: 1000, MaxOutputTokens: 100}, nil)
	plainOut := plain.Pre(context.Background(), msgs, 0)
	for i := range tools {
		if got := toolResultText(plainOut[i*2+1]); got == microClearedMessage {
			t.Fatalf("default config unexpectedly cleared tool %d (%s)", i, tools[i])
		}
	}
}
