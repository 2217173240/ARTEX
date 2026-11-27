package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 连接测试的正常回复:模型只回一句 OK。
const (
	conntestOKChatJSON      = `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	conntestOKAnthropicJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	conntestOKChatSSE       = `data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"}}]}` + "\n\n" +
		`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		`data: [DONE]` + "\n\n"
	// anthropic 的 SSE 解析只看 data 里的 type,不需要 event: 行。
	conntestOKAnthropicSSE = `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
		`data: {"type":"message_stop"}` + "\n\n"
)

// conntestServer 起一个假的模型端点:记录收到的请求体,按给定 Content-Type 回固定内容。
func conntestServer(t *testing.T, contentType, reply string, body *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 连接测试必须发一条不带工具的裸聊天请求。测试跑在 ModeBypass 下:请求里只要出现
// 工具(SDK 在 Tools 为 nil 时装入的内置文件/shell 工具,或它自动追加的
// TaskOutput/TaskStop/TaskList/Monitor),模型就可能去调它,而不是输出 OK。
func TestConnectionSendsNoTools(t *testing.T) {
	for _, tc := range []struct {
		name, provider, contentType, reply string
		stream                             bool
	}{
		{"openai_nonstream", "openai", "application/json", conntestOKChatJSON, false},
		{"openai_stream", "openai", "text/event-stream", conntestOKChatSSE, true},
		{"anthropic_nonstream", "anthropic", "application/json", conntestOKAnthropicJSON, false},
		{"anthropic_stream", "anthropic", "text/event-stream", conntestOKAnthropicSSE, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			srv := conntestServer(t, tc.contentType, tc.reply, &body)

			cfg := ConfigFrom(tc.provider, "m", srv.URL+"/v1", "", "")
			cfg.Stream = tc.stream
			_, reply, err := TestConnection(context.Background(), cfg)
			if err != nil || reply != "OK" {
				t.Fatalf("TestConnection = %q, %v; want OK", reply, err)
			}
			var req map[string]json.RawMessage
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("request body: %v\n%s", err, body)
			}
			// 两种协议都靠 omitempty 省掉空列表,所以键必须整个不出现;
			// 出现了(哪怕带一个工具)都说明测试把工具交给了模型。
			if raw, ok := req["tools"]; ok {
				t.Fatalf("connection test sent tools, want none:\n%s", raw)
			}
		})
	}
}

// 模型回工具调用(带不带文字都一样)、以 tool_calls/tool_use 结束、或只回空白,
// 都必须判连接测试失败,不能报成功。
func TestConnectionRejectsToolCallReplies(t *testing.T) {
	for _, tc := range []struct {
		name, provider, contentType, reply, wantErr string
		stream                                      bool
	}{
		{
			name:        "openai_nonstream_tool_call_with_text",
			provider:    "openai",
			contentType: "application/json",
			reply:       `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"OK","tool_calls":[{"id":"call_1","type":"function","function":{"name":"TaskOutput","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}}`,
			wantErr:     "工具调用",
		},
		{
			name:        "openai_nonstream_tool_call_only",
			provider:    "openai",
			contentType: "application/json",
			reply:       `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"TaskOutput","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}}`,
			wantErr:     "工具调用",
		},
		{
			name:        "openai_nonstream_finish_reason_tool_calls_without_array",
			provider:    "openai",
			contentType: "application/json",
			reply:       `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`,
			wantErr:     "工具调用",
		},
		{
			name:        "openai_stream_tool_call_with_text",
			provider:    "openai",
			contentType: "text/event-stream",
			stream:      true,
			reply: strings.Join([]string{
				`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"}}]}`,
				`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"TaskOutput","arguments":"{}"}}]}}]}`,
				`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				`data: [DONE]`,
				``,
			}, "\n\n"),
			wantErr: "工具调用",
		},
		{
			name:        "anthropic_nonstream_tool_use",
			provider:    "anthropic",
			contentType: "application/json",
			reply:       `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"OK"},{"type":"tool_use","id":"toolu_1","name":"TaskOutput","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`,
			wantErr:     "工具调用",
		},
		{
			name:        "anthropic_stream_tool_use",
			provider:    "anthropic",
			contentType: "text/event-stream",
			stream:      true,
			reply: strings.Join([]string{
				`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"TaskOutput"}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
				`data: {"type":"message_stop"}`,
				``,
			}, "\n\n"),
			wantErr: "工具调用",
		},
		{
			name:        "openai_whitespace_only_content",
			provider:    "openai",
			contentType: "application/json",
			reply:       `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"   \n\t  "},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}`,
			wantErr:     "模型无回复内容",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			srv := conntestServer(t, tc.contentType, tc.reply, &body)

			cfg := ConfigFrom(tc.provider, "m", srv.URL+"/v1", "", "")
			cfg.Stream = tc.stream
			_, reply, err := TestConnection(context.Background(), cfg)
			if err == nil {
				t.Fatalf("expected TestConnection to reject %s, got Connected with reply=%q", tc.name, reply)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("unexpected error for %s: %v (want substring %q)", tc.name, err, tc.wantErr)
			}
		})
	}
}

func TestIsToolCallStopReason(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"tool_use", true},
		{"tool_calls", true},
		{" Tool_Calls ", true},
		{"end_turn", false},
		{"stop", false},
		{"length", false},
		{"", false},
	} {
		if got := isToolCallStopReason(tc.in); got != tc.want {
			t.Errorf("isToolCallStopReason(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
