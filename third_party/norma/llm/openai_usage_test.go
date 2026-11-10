package llm

import (
	"fmt"
	"testing"
)

func TestOpenAIUsageAliases(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		cache        int
	}{
		{"canonical", `"prompt_tokens_details":{"cached_tokens":40}`, 40},
		{"deepseek", `"prompt_cache_hit_tokens":40`, 40},
		{"opencode", `"opencode":{"tokens":{"cache_read":40}}`, 40},
		{"canonical zero", `"prompt_tokens_details":{"cached_tokens":0},"prompt_cache_hit_tokens":40,"opencode":{"tokens":{"cache_read":60}}`, 0},
		{"deepseek zero", `"prompt_cache_hit_tokens":0,"opencode":{"tokens":{"cache_read":60}}`, 0},
		{"canonical null", `"prompt_tokens_details":{"cached_tokens":null},"prompt_cache_hit_tokens":40`, 40},
		{"invalid alias fallback", `"prompt_cache_hit_tokens":"bad","opencode":{"tokens":{"cache_read":40}}`, 40},
		{"negative alias fallback", `"prompt_cache_hit_tokens":-1,"opencode":{"tokens":{"cache_read":40}}`, 40},
		{"invalid extensions", `"prompt_cache_hit_tokens":{},"opencode":[]`, 0},
		{"malformed nested extension", `"opencode":{"tokens":"bad"}`, 0},
		{"null aliases", `"prompt_cache_hit_tokens":null,"opencode":null`, 0},
		{"negative canonical preserved", `"prompt_tokens_details":{"cached_tokens":-2},"prompt_cache_hit_tokens":40`, -2},
		{"valid canonical malformed extensions", `"prompt_tokens_details":{"cached_tokens":40},"prompt_cache_hit_tokens":[],"opencode":"bad"`, 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usageJSON := fmt.Sprintf(`{"prompt_tokens":100,"completion_tokens":9,%s}`, tc.fields)
			want := Usage{InputTokens: 100, OutputTokens: 9, CacheReadTokens: tc.cache}
			msg, stop, got, err := parseOpenAIResponse([]byte(`{"choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"usage":` + usageJSON + `}`))
			if err != nil || got != want || stop != "end_turn" || len(msg.Content) != 1 || msg.Content[0].Text != "hello" {
				t.Fatalf("response: msg=%+v stop=%s usage=%+v err=%v", msg, stop, got, err)
			}
			evs, stop, got, ok := parseOpenAIFrame(`{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}],"usage":`+usageJSON+`}`, map[int]bool{})
			if !ok || got != want || stop != "end_turn" || len(evs) != 1 || evs[0].Text != "hello" {
				t.Fatalf("frame: events=%+v stop=%s usage=%+v ok=%v", evs, stop, got, ok)
			}
		})
	}
}

func TestOpenAIUsageSnapshots(t *testing.T) {
	var snapshot Usage
	for _, tc := range []struct {
		raw     string
		want    Usage
		present bool
	}{
		{`{"prompt_tokens":100,"completion_tokens":5,"prompt_cache_hit_tokens":40}`, Usage{InputTokens: 100, OutputTokens: 5, CacheReadTokens: 40}, true},
		{`{"completion_tokens":9}`, Usage{InputTokens: 100, OutputTokens: 9, CacheReadTokens: 40}, true},
		{`{"completion_tokens":9}`, Usage{InputTokens: 100, OutputTokens: 9, CacheReadTokens: 40}, true},
		{`{"prompt_tokens":null,"completion_tokens":null,"prompt_tokens_details":{"cached_tokens":null}}`, Usage{InputTokens: 100, OutputTokens: 9, CacheReadTokens: 40}, true},
		{`{}`, Usage{InputTokens: 100, OutputTokens: 9, CacheReadTokens: 40}, true},
		{`null`, Usage{InputTokens: 100, OutputTokens: 9, CacheReadTokens: 40}, false},
		{`{"prompt_tokens":80,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":30}}`, Usage{InputTokens: 80, OutputTokens: 7, CacheReadTokens: 30}, true},
		{`{"prompt_tokens":0,"completion_tokens":0,"prompt_tokens_details":{"cached_tokens":0},"prompt_cache_hit_tokens":40}`, Usage{}, true},
		{`{"prompt_tokens":-1,"completion_tokens":-2,"prompt_tokens_details":{"cached_tokens":-3}}`, Usage{InputTokens: -1, OutputTokens: -2, CacheReadTokens: -3}, true},
	} {
		_, _, got, ok := parseOpenAIFrameSnapshot(`{"choices":[],"usage":`+tc.raw+`}`, map[int]bool{}, snapshot)
		if ok {
			snapshot = got
		}
		if ok != tc.present || snapshot != tc.want {
			t.Fatalf("%s: got %+v present=%v want %+v", tc.raw, snapshot, ok, tc.want)
		}
	}
	_, _, independent, ok := parseOpenAIFrameSnapshot(`{"choices":[],"usage":{"completion_tokens":2}}`, map[int]bool{}, Usage{})
	if !ok || independent != (Usage{OutputTokens: 2}) {
		t.Fatalf("independent snapshot: %+v", independent)
	}
}
