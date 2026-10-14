package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestContinuityPublicSendPaths(t *testing.T) {
	requests := make(chan []oaMessage, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream   bool                         `json:"stream"`
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		var msgs []oaMessage
		for _, m := range body.Messages {
			raw, _ := json.Marshal(m)
			var msg oaMessage
			_ = json.Unmarshal(raw, &msg)
			msgs = append(msgs, msg)
			if msg.Role == "tool" && msg.ToolCallID == "b" {
				if raw, ok := m["content"]; !ok || string(raw) != `""` {
					t.Errorf("empty result content=%s, present=%v", raw, ok)
				}
			}
		}
		requests <- msgs
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		}
	}))
	defer srv.Close()
	p, err := NewProvider(Config{Format: FormatOpenAI, BaseURL: srv.URL, APIKey: "fixture", Model: "fixture", HTTPClient: srv.Client(), MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	messages := []Message{
		{Role: RoleAssistant, Content: []ContentBlock{{Type: BlockThinking, Thinking: "original reasoning", Signature: "source signature"}, {Type: BlockToolUse, ID: "a", Name: "Read"}, {Type: BlockToolUse, ID: "b", Name: "Read"}}},
		{Role: RoleUser, Content: []ContentBlock{{Type: BlockToolResult, ToolUseID: "b"}, {Type: BlockToolResult, ToolUseID: "a", Content: []ContentBlock{TextBlock("original output")}}}},
		toolUseMsg("interrupted"), UserText("summary"), {Role: RoleUser, Content: []ContentBlock{{Type: BlockToolResult, ToolUseID: "interrupted", Content: []ContentBlock{TextBlock("late output")}}}},
	}
	before, _ := json.Marshal(messages)
	req := CompletionRequest{Messages: messages, MaxTokens: 100}
	if _, _, _, err := p.Complete(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	for _, err := range p.Stream(t.Context(), req) {
		if err != nil {
			t.Fatal(err)
		}
	}
	after, _ := json.Marshal(messages)
	if string(before) != string(after) {
		t.Fatal("public send path mutated original history")
	}
	complete, stream := <-requests, <-requests
	if !reflect.DeepEqual(complete, stream) {
		t.Fatal("send paths encode different message sequences")
	}
	assertOpenAIToolOrder(t, complete)
	if len(complete[0].ToolCalls) != 2 || complete[0].ReasoningContent != "original reasoning" || complete[1].ToolCallID != "b" || complete[1].Content != "" || complete[2].Content != "original output" {
		t.Fatalf("valid exchange changed: %+v", complete)
	}
	if countRole(complete, "tool") != 2 {
		t.Fatalf("late result survived: %+v", complete)
	}
}
