package intercept

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestJSONRegexPayloadStrings(t *testing.T) {
	r := compiledRule{InterceptRule: db.InterceptRule{MatchTarget: "tool_input", MatchType: "regex"}, re: regexp.MustCompile(`rm\s+-rf`)}
	for _, tc := range []struct {
		name, input string
		want        bool
	}{
		{"plain_raw", `{"command":"rm -rf /tmp/ARTEX-NO-EXEC"}`, true},
		{"unicode", `{"command":"\u0072m -rf /tmp/ARTEX-NO-EXEC"}`, true},
		{"leading_whitespace", " \n\t" + `{"command":"\u0072m -rf /tmp/ARTEX-NO-EXEC"}`, true},
		{"nested", `{"args":[null,42,{"command":"\u0072m\t-rf fixture"}]}`, true},
		{"top_level_string", `"\u0072m -rf fixture"`, true},
		{"large_number", `{"number":1e9999,"command":"\u0072m -rf fixture"}`, true},
		{"field_boundary", `{"a":"\u0072m ","b":"-rf fixture"}`, false},
		{"array_boundary", `["\u0072m ","-rf fixture"]`, false},
		{"decoded_key_excluded", `{"\u0072m -rf fixture":"safe"}`, false},
		{"non_json_not_unescaped", `command=\u0072m -rf fixture`, false},
		{"invalid_json", `{"command":"\u0072m -rf fixture"`, false},
		{"trailing_json", `{"command":"\u0072m -rf fixture"} {}`, false},
		{"double_escape", `{"command":"\\u0072m -rf fixture"}`, false},
		{"no_string", `[null,true,42]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := json.RawMessage(tc.input)
			original := append([]byte(nil), input...)
			if got := ruleMatches(r, "Bash", input); got != tc.want {
				t.Fatalf("match=%v want=%v", got, tc.want)
			}
			if !bytes.Equal(input, original) {
				t.Fatal("raw input mutated")
			}
		})
	}
	// The old raw-only predicate misses the exact Unicode regression fixture.
	input := []byte(`{"command":"\u0072m -rf fixture"}`)
	if r.re.Match(input) || !ruleMatches(r, "Bash", input) {
		t.Fatal("fixture must distinguish raw-only and decoded matching")
	}
}

func TestJSONRegexPreservesRuleTargets(t *testing.T) {
	input := []byte(`{"command":"\u0072m -rf fixture"}`)
	r := compiledRule{InterceptRule: db.InterceptRule{MatchTarget: "tool_input", MatchType: "regex"}, re: regexp.MustCompile(`^\{"command":`)}
	if !ruleMatches(r, "Bash", input) {
		t.Fatal("raw JSON regex lost")
	}
	r.re = regexp.MustCompile(`^rm -rf$`)
	r.MatchTarget = "tool_name"
	if ruleMatches(r, "Bash", input) || !ruleMatches(r, "rm -rf", []byte(`{}`)) {
		t.Fatal("tool-name scope changed")
	}
	r.MatchType, r.Pattern, r.MatchTarget = "contains", "rm -rf", "tool_input"
	if ruleMatches(r, "Bash", input) {
		t.Fatal("contains rules changed")
	}
	r.MatchType, r.re = "regex", nil
	if ruleMatches(r, "Bash", input) {
		t.Fatal("nil regex matched")
	}
}

func TestJSONRegexPreservesRuleChainAndJudge(t *testing.T) {
	rules := []db.InterceptRule{
		{ID: 1, Enabled: false, Priority: 300, MatchTarget: "tool_input", MatchType: "regex", Pattern: `rm\s+-rf`, Action: "deny"},
		{ID: 2, Enabled: true, Priority: 200, MatchTarget: "tool_input", MatchType: "regex", Pattern: `rm\s+-rf`, Action: "ask"},
		{ID: 3, Enabled: true, Priority: 100, MatchTarget: "tool_input", MatchType: "regex", Pattern: `rm\s+-rf`, Action: "deny"},
	}
	s := &configTestStore{rules: rules, settings: map[string]string{settingJudgeEnabled: "true"}}
	ic := New(s.open(t))
	input := json.RawMessage(`{"command":"\u0072m -rf fixture"}`)
	dec, matched, err := ic.Match("Bash", input)
	configJSON, _ := json.Marshal(rules[1])
	if err != nil || !matched || dec.RuleID != 2 || dec.Action != "ask" || dec.ConfigDigest != digestInput(configJSON) {
		t.Fatalf("first enabled rule or digest changed: %+v matched=%v err=%v", dec, matched, err)
	}
	input = json.RawMessage(`{"command":"echo fixture"}`)
	if _, matched, err := ic.Match("Bash", input); err != nil || matched {
		t.Fatalf("expected non-match: %v %v", matched, err)
	}
	called := false
	ic.SetReviewer(func(_ context.Context, _ int64, _ string, in ReviewInput) (Decision, error) {
		called = true
		if !bytes.Equal(in.Arguments, input) {
			t.Fatal("judge did not receive original raw input")
		}
		return Decision{Action: "deny"}, nil
	})
	dec, judged, err := ic.Judge(t.Context(), "Bash", input)
	if err != nil || !judged || !called || dec.Action != "deny" || dec.RuleID != 0 {
		t.Fatalf("fallback changed: %+v judged=%v called=%v err=%v", dec, judged, called, err)
	}
}
