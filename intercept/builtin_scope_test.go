package intercept

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestDefaultEnabledToolsIncludesMonitor(t *testing.T) {
	ic := New((&configTestStore{}).open(t))
	tools, err := ic.GetEnabledTools()
	if err != nil {
		t.Fatalf("GetEnabledTools error: %v", err)
	}
	if !slices.Contains(tools, "Monitor") {
		t.Fatalf("defaultEnabledTools = %v, want Monitor included", tools)
	}
	enabled, err := ic.IsToolEnabled("Monitor")
	if err != nil || !enabled {
		t.Fatalf("IsToolEnabled(Monitor) = (%v, %v), want (true, nil)", enabled, err)
	}
}

func TestBuiltinToolInputRulesScopedAwayFromFileEdits(t *testing.T) {
	now := time.Now()
	rules := []db.InterceptRule{
		{
			ID:          1,
			Name:        "[内置] 递归强制删除 rm -rf",
			Enabled:     true,
			Priority:    100,
			MatchTarget: "tool_input",
			MatchType:   "regex",
			Pattern:     `(?i)\brm\b.{0,80}(?:-[a-z]*r[a-z]*f[a-z]*|-[a-z]*f[a-z]*r[a-z]*|--recursive|--no-preserve-root)`,
			Action:      "deny",
			Message:     "禁止 rm -rf",
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          2,
			Name:        "[内置] 删除类接口路径",
			Enabled:     true,
			Priority:    80,
			MatchTarget: "tool_input",
			MatchType:   "regex",
			Pattern:     `(?i)/(?:(?:delete|remove|unlink|erase|destroy)[-\w]*|del)(?:[/?#"'\s]|$)`,
			Action:      "deny",
			Message:     "禁止删除类接口",
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          3,
			Name:        "自定义敏感写入拦截",
			Enabled:     true,
			Priority:    70,
			MatchTarget: "tool_input",
			MatchType:   "contains",
			Pattern:     "CUSTOM_FORBIDDEN_MARKER",
			Action:      "deny",
			Message:     "自定义规则拦截",
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}

	s := &configTestStore{
		rules:    rules,
		settings: map[string]string{settingJudgeEnabled: "true"},
	}
	ic := New(s.open(t))

	// (a) [内置] rules still deny execution and network tools.
	for _, tc := range []struct {
		tool  string
		input string
		rule  int64
	}{
		{"Bash", `{"command":"rm -rf /tmp/artifact"}`, 1},
		{"shell_open", `{"command":"rm -rf /tmp/artifact"}`, 1},
		{"Monitor", `{"command":"curl -s http://target/api/user/delete?id=1"}`, 2},
		{"WebFetch", `{"url":"https://target.example/api/user/delete?id=1"}`, 2},
	} {
		dec, matched, err := ic.Match(tc.tool, json.RawMessage(tc.input))
		if err != nil || !matched || dec.Action != "deny" || dec.RuleID != tc.rule {
			t.Fatalf("%s should be denied by rule %d: matched=%v dec=%+v err=%v", tc.tool, tc.rule, matched, dec, err)
		}
	}

	// (b) [内置] tool_input rules allow Write/Edit/MultiEdit writing PoCs/reports mentioning rm -rf or /delete.
	for _, tc := range []struct {
		tool  string
		input string
	}{
		{"Write", `{"file_path":"report.md","content":"PoC: GET /api/user/delete?id=1 and rm -rf /tmp/test"}`},
		{"Edit", `{"file_path":"report.md","old_string":"a","new_string":"Note: avoid \u0072m -rf and /delete"}`},
		{"MultiEdit", `{"file_path":"report.md","edits":[{"old_string":"x","new_string":"GET /api/delete"}]}`},
	} {
		dec, matched, err := ic.Match(tc.tool, json.RawMessage(tc.input))
		if err != nil || matched {
			t.Fatalf("%s should not match built-in tool_input rules: matched=%v dec=%+v err=%v", tc.tool, matched, dec, err)
		}
	}

	// (c) Custom user-created rule on tool_input still denies Write/Edit/MultiEdit,
	// and unmatched Write calls remain eligible for the LLM judge.
	for _, tool := range []string{"Write", "Edit", "MultiEdit"} {
		dec, matched, err := ic.Match(tool, json.RawMessage(`{"file_path":"out.txt","content":"has CUSTOM_FORBIDDEN_MARKER"}`))
		if err != nil || !matched || dec.Action != "deny" || dec.RuleID != 3 {
			t.Fatalf("%s should be denied by custom rule 3: matched=%v dec=%+v err=%v", tool, matched, dec, err)
		}
	}

	judgeCalled := false
	ic.SetReviewer(func(_ context.Context, _ int64, _ string, in ReviewInput) (Decision, error) {
		judgeCalled = true
		if in.Tool != "Write" {
			t.Fatalf("unexpected judge tool: %q", in.Tool)
		}
		return Decision{Action: "allow", Message: "safe report edit"}, nil
	})
	dec, judged, err := ic.Judge(t.Context(), "Write", json.RawMessage(`{"file_path":"report.md","content":"mentions /delete"}`))
	if err != nil || !judged || !judgeCalled || dec.Action != "allow" {
		t.Fatalf("Write judge fallback changed: judged=%v called=%v dec=%+v err=%v", judged, judgeCalled, dec, err)
	}
}
