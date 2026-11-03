package llm

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestOpenAIToolSignatureWireResponseReplay(t *testing.T) {
	raw := []byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"call0","type":"function","function":{"name":"lookup","arguments":"{\"key\":0}"},"extra_content":{"google":{"thought_signature":"opaque+/= zero"}}},{"id":"call1","type":"function","function":{"name":"lookup","arguments":"{\"key\":1}"},"extra_content":{"google":{"thought_signature":"opaque+/= one"}}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":3}}}`)
	msg, stop, usage, err := parseOpenAIResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if stop != "tool_use" || usage != (Usage{InputTokens: 12, OutputTokens: 7, CacheReadTokens: 3}) {
		t.Fatalf("stop=%q usage=%+v", stop, usage)
	}
	assertOpenAISignatureReplay(t, msg)
}

func TestOpenAIToolSignatureWireStreamReplay(t *testing.T) {
	frames := []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"extra_content":{"google":{"thought_signature":"opaque+/= one"}}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call0","type":"function","function":{"name":"lookup","arguments":"{\"key\":"}},{"index":1,"id":"call1","type":"function","function":{"name":"lookup","arguments":"{\"key\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"1}"}},{"index":0,"function":{"arguments":"0}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"extra_content":{"google":{"thought_signature":"opaque+/= zero"}}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":3}}}`,
	}
	acc := NewAccumulator()
	started := map[int]bool{}
	for n, frame := range frames {
		events, stop, usage, hasUsage := parseOpenAIFrame(frame, started)
		if n == 0 && (len(events) != 1 || events[0].Type != SEToolUseSignature || started[1]) {
			t.Fatalf("signature-only early frame events=%+v started=%v", events, started)
		}
		for _, event := range events {
			if event.ToolIndex == nil {
				t.Fatalf("tool event lacks index: %+v", event)
			}
			acc.Add(event)
		}
		if stop != "" || hasUsage {
			acc.Add(StreamEvent{Type: SEMessageDelta, StopReason: stop, Usage: usage})
		}
	}
	if acc.StopReason != "tool_use" || acc.Usage != (Usage{InputTokens: 12, OutputTokens: 7, CacheReadTokens: 3}) {
		t.Fatalf("stop=%q usage=%+v", acc.StopReason, acc.Usage)
	}
	assertOpenAISignatureReplay(t, acc.Message())
}

func assertOpenAISignatureReplay(t *testing.T, msg Message) {
	t.Helper()
	want := []ContentBlock{
		{Type: BlockToolUse, ID: "call0", Name: "lookup", Input: json.RawMessage(`{"key":0}`), ThoughtSignature: "opaque+/= zero"},
		{Type: BlockToolUse, ID: "call1", Name: "lookup", Input: json.RawMessage(`{"key":1}`), ThoughtSignature: "opaque+/= one"},
	}
	if !reflect.DeepEqual(msg.Content, want) {
		t.Fatalf("blocks=%+v want=%+v", msg.Content, want)
	}
	history := []Message{msg, {Role: RoleUser, Content: []ContentBlock{ToolResultText("call0", "zero", false), ToolResultText("call1", "one", false)}}}
	raw, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	var restored []Message
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, history) {
		t.Fatalf("history changed through JSON: %s", raw)
	}
	wire := sanitizeOpenAIMessages(toOpenAIMessages("", restored), false)
	raw, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []oaMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 3 || len(decoded[0].ToolCalls) != 2 || decoded[1].ToolCallID != "call0" || decoded[2].ToolCallID != "call1" {
		t.Fatalf("replay pairing=%s", raw)
	}
	for i, call := range decoded[0].ToolCalls {
		if call.ID != want[i].ID || call.Function.Arguments != string(want[i].Input) || call.ExtraContent == nil || call.ExtraContent.Google.ThoughtSignature != want[i].ThoughtSignature {
			t.Fatalf("replay call %d=%+v JSON=%s", i, call, raw)
		}
	}
}

func TestOpenAIToolSignatureWireUnsignedCompatibility(t *testing.T) {
	msg, _, _, err := parseOpenAIResponse([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"plain","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	wire := toOpenAIMessages("", []Message{msg})
	if len(wire) != 1 || len(wire[0].ToolCalls) != 1 || wire[0].ToolCalls[0].ExtraContent != nil || msg.Content[0].ThoughtSignature != "" {
		t.Fatalf("unsigned response gained signature: %+v", wire)
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	call := decoded[0]["tool_calls"].([]any)[0].(map[string]any)
	if _, ok := call["extra_content"]; ok {
		t.Fatalf("unsigned replay includes extra_content: %s", raw)
	}
	acc := NewAccumulator()
	events, _, _, _ := parseOpenAIFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"plain","function":{"name":"lookup","arguments":"{}"}}]}}]}`, map[int]bool{})
	for _, event := range events {
		if event.Type == SEToolUseSignature {
			t.Fatal("unsigned frame emitted signature")
		}
		acc.Add(event)
	}
	if !reflect.DeepEqual(acc.Message(), msg) {
		t.Fatalf("unsigned stream=%+v response=%+v", acc.Message(), msg)
	}
}
