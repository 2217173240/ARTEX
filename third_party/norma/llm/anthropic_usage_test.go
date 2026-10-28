package llm

import (
	"encoding/json"
	"testing"
)

func TestAnthropicUsageSnapshots(t *testing.T) {
	tests := []struct {
		name   string
		start  string
		deltas []string
		want   Usage
	}{
		{"official basic example", `{"input_tokens":25,"output_tokens":1}`, []string{`{"output_tokens":15}`}, Usage{InputTokens: 25, OutputTokens: 15}},
		{"cumulative repeated", `{"input_tokens":25,"output_tokens":1}`, []string{`{"output_tokens":4}`, `{"output_tokens":9}`, `{"output_tokens":9}`}, Usage{InputTokens: 25, OutputTokens: 9}},
		{"late cache correction", `{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":20}`, []string{`{"input_tokens":10,"cache_read_input_tokens":80,"cache_creation_input_tokens":30,"output_tokens":9}`}, Usage{InputTokens: 120, OutputTokens: 9, CacheReadTokens: 80, CacheWriteTokens: 30}},
		{"explicit zero", `{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":20}`, []string{`{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":9}`, `{"output_tokens":9}`}, Usage{OutputTokens: 9}},
		{"null and omitted preserve", `{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":20}`, []string{`{"input_tokens":null,"cache_read_input_tokens":null,"output_tokens":9}`, `{}`, `null`}, Usage{InputTokens: 160, OutputTokens: 9, CacheReadTokens: 40, CacheWriteTokens: 20}},
		{"downward output correction", `{"input_tokens":25}`, []string{`{"output_tokens":9}`, `{"output_tokens":0}`}, Usage{InputTokens: 25}},
		{"start only compatibility", `{"input_tokens":100,"cache_read_input_tokens":40,"output_tokens":1}`, nil, Usage{InputTokens: 140, CacheReadTokens: 40}},
		{"missing initial usage", `null`, []string{`{"input_tokens":25,"output_tokens":9}`}, Usage{InputTokens: 25, OutputTokens: 9}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// 每次解码流使用独立快照，并使用实际 Accumulator 折叠事件。
			for attempt := 0; attempt < 2; attempt++ {
				var snapshot anthropicUsage
				acc := NewAccumulator()
				fold := func(kind string, usage string) {
					t.Helper()
					frame := struct {
						Type    string `json:"type"`
						Message *struct {
							Usage json.RawMessage `json:"usage"`
						} `json:"message,omitempty"`
						Usage json.RawMessage `json:"usage,omitempty"`
					}{Type: kind}
					if kind == "message_start" {
						frame.Message = &struct {
							Usage json.RawMessage `json:"usage"`
						}{json.RawMessage(usage)}
					} else {
						frame.Usage = json.RawMessage(usage)
					}
					raw, err := json.Marshal(frame)
					if err != nil {
						t.Fatal(err)
					}
					ev, ok, err := parseAnthropicFrame(string(raw), &snapshot)
					if err != nil {
						t.Fatal(err)
					}
					if ok {
						acc.Add(ev)
					}
				}
				fold("message_start", tt.start)
				for _, delta := range tt.deltas {
					fold("message_delta", delta)
				}
				if acc.Usage != tt.want {
					t.Fatalf("attempt %d usage=%+v want=%+v", attempt, acc.Usage, tt.want)
				}
			}
		})
	}
}

func TestAnthropicUsagePreservesOtherEvents(t *testing.T) {
	var snapshot anthropicUsage
	acc := NewAccumulator()
	frames := []string{
		`{"type":"message_start","message":{"usage":{"input_tokens":25,"output_tokens":1}}}`,
		`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"reason"}}`,
		`{"type":"content_block_delta","delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}`,
		`{"type":"content_block_start","content_block":{"type":"tool_use","id":"tool1","name":"lookup"}}`,
		`{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"key\":1}"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		`{"type":"message_stop"}`,
	}
	for _, raw := range frames {
		ev, ok, err := parseAnthropicFrame(raw, &snapshot)
		if err != nil || !ok {
			t.Fatalf("decode ok=%v err=%v", ok, err)
		}
		acc.Add(ev)
	}
	msg := acc.Message()
	if msg.Text() != "hello" || acc.StopReason != "tool_use" || len(msg.Content) != 3 || msg.Content[0].Thinking != "reason" || msg.Content[0].Signature != "sig" || msg.Content[2].ID != "tool1" || string(msg.Content[2].Input) != `{"key":1}` {
		t.Fatalf("message=%+v stop=%q", msg, acc.StopReason)
	}
	_, stop, usage, err := parseAnthropicResponse([]byte(`{"type":"message","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"cache_read_input_tokens":80,"cache_creation_input_tokens":30,"output_tokens":9}}`))
	if err != nil || stop != "end_turn" || usage != (Usage{InputTokens: 120, OutputTokens: 9, CacheReadTokens: 80, CacheWriteTokens: 30}) {
		t.Fatalf("nonstream usage=%+v stop=%q err=%v", usage, stop, err)
	}
}

func TestAnthropicUsageIndependentStreams(t *testing.T) {
	var snapshots [2]anthropicUsage
	accs := [2]*Accumulator{NewAccumulator(), NewAccumulator()}
	for _, frame := range []struct {
		stream int
		raw    string
	}{
		{0, `{"type":"message_start","message":{"usage":{"input_tokens":25}}}`},
		{1, `{"type":"message_start","message":{"usage":{"input_tokens":100}}}`},
		{0, `{"type":"message_delta","usage":{"output_tokens":4}}`},
		{1, `{"type":"message_delta","usage":{"output_tokens":20}}`},
		{0, `{"type":"message_delta","usage":{"output_tokens":9}}`},
	} {
		ev, ok, err := parseAnthropicFrame(frame.raw, &snapshots[frame.stream])
		if err != nil || !ok {
			t.Fatalf("decode ok=%v err=%v", ok, err)
		}
		accs[frame.stream].Add(ev)
	}
	if accs[0].Usage != (Usage{InputTokens: 25, OutputTokens: 9}) || accs[1].Usage != (Usage{InputTokens: 100, OutputTokens: 20}) {
		t.Fatalf("stream usage=%+v and %+v", accs[0].Usage, accs[1].Usage)
	}
}
