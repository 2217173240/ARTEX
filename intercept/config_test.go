package intercept

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Use the real database/sql query and Scan paths with deterministic local
// failures. No default DSN, network connection, or external provider is used.
type configTestStore struct {
	rules        []db.InterceptRule
	settings     map[string]string
	rulesErr     error
	settingErrs  map[string]error
	rulesReads   int
	settingReads int
}

func (s *configTestStore) open(t *testing.T) *db.DB {
	t.Helper()
	d := sql.OpenDB(configTestConnector{s})
	t.Cleanup(func() { _ = d.Close() })
	return &db.DB{DB: d}
}

type configTestConnector struct{ store *configTestStore }

func (c configTestConnector) Connect(context.Context) (driver.Conn, error) {
	return configTestConn{c.store}, nil
}
func (c configTestConnector) Driver() driver.Driver { return configTestDriver{} }

type configTestDriver struct{}

func (configTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}

type configTestConn struct{ store *configTestStore }

func (configTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (configTestConn) Close() error              { return nil }
func (configTestConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (c configTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.store
	if strings.Contains(query, "FROM intercept_rules") {
		s.rulesReads++
		if s.rulesErr != nil {
			return nil, s.rulesErr
		}
		rows := &configTestRows{columns: strings.Split("id,name,enabled,priority,match_target,match_type,pattern,action,message,timeout_enabled,timeout_seconds,timeout_action,created_at,updated_at", ",")}
		for _, r := range s.rules {
			rows.values = append(rows.values, []driver.Value{r.ID, r.Name, r.Enabled, int64(r.Priority), r.MatchTarget, r.MatchType, r.Pattern, r.Action, r.Message, r.TimeoutEnabled, int64(r.TimeoutSeconds), r.TimeoutAction, r.CreatedAt, r.UpdatedAt})
		}
		return rows, nil
	}
	if strings.Contains(query, "FROM settings") {
		s.settingReads++
		key := args[0].Value.(string)
		if err := s.settingErrs[key]; err != nil {
			return nil, err
		}
		rows := &configTestRows{columns: []string{"value"}}
		if v, ok := s.settings[key]; ok {
			rows.values = [][]driver.Value{{v}}
		}
		return rows, nil
	}
	return nil, errors.New("unexpected query")
}

type configTestRows struct {
	columns []string
	values  [][]driver.Value
}

func (r *configTestRows) Columns() []string { return r.columns }
func (r *configTestRows) Close() error      { return nil }
func (r *configTestRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}

func TestLoadConfigDoesNotPublishPartialCache(t *testing.T) {
	readErr := errors.New("settings unavailable")
	s := &configTestStore{
		rules:       []db.InterceptRule{{ID: 1, Enabled: true, MatchTarget: "tool_name", MatchType: "contains", Pattern: "Bash", Action: "deny", CreatedAt: time.Now(), UpdatedAt: time.Now()}},
		settingErrs: map[string]error{"intercept_enabled_tools": readErr},
	}
	ic := New(s.open(t))
	if err := ic.loadLocked(); !errors.Is(err, readErr) {
		t.Fatalf("settings failure was ignored: %v", err)
	}
	if ic.cached != nil || ic.enabledTools != nil {
		t.Fatal("failed configuration load published a partial cache")
	}
	delete(s.settingErrs, "intercept_enabled_tools")
	s.settings = map[string]string{"intercept_enabled_tools": `["Bash"]`}
	s.rules[0].Action = "ask"
	dec, matched, err := ic.Match("Bash", []byte(`{}`))
	if err != nil || !matched || dec.Action != "ask" || s.rulesReads != 2 {
		t.Fatalf("failed load was not retried with the complete configuration: %+v matched=%v reads=%d err=%v", dec, matched, s.rulesReads, err)
	}
}

func TestEmptyRulesAreCached(t *testing.T) {
	s := &configTestStore{}
	ic := New(s.open(t))
	for range 2 {
		enabled, err := ic.IsToolEnabled("Bash")
		if !enabled || err != nil {
			t.Fatalf("default enabled tools were lost: enabled=%v err=%v", enabled, err)
		}
		_, matched, err := ic.Match("Bash", []byte(`{}`))
		if matched || err != nil {
			t.Fatalf("empty rules must be a successful non-match: matched=%v err=%v", matched, err)
		}
	}
	if s.rulesReads != 1 || s.settingReads != 1 || ic.cached == nil || ic.enabledTools == nil {
		t.Fatalf("valid empty rules were not cached: rule reads=%d setting reads=%d", s.rulesReads, s.settingReads)
	}
}

func TestEnabledToolsConfigDistinguishesDisabledAndInvalid(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		enabled     bool
		invalid     bool
	}{
		{name: "explicit_empty", value: `[]`},
		{name: "configured", value: `["Bash"]`, enabled: true},
		{name: "other_tool", value: `["Write"]`},
		{name: "broken_json", value: `[`, invalid: true},
		{name: "wrong_type", value: `{}`, invalid: true},
		{name: "null", value: `null`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &configTestStore{settings: map[string]string{"intercept_enabled_tools": tc.value}}
			ic := New(s.open(t))
			enabled, err := ic.IsToolEnabled("Bash")
			if (err != nil) != tc.invalid || enabled != tc.enabled {
				t.Fatalf("enabled=%v err=%v", enabled, err)
			}
			if tc.invalid && (ic.cached != nil || ic.enabledTools != nil) {
				t.Fatal("invalid tool configuration was published")
			}
			_, err = ic.GetEnabledTools()
			if (err != nil) != tc.invalid {
				t.Fatalf("GET hid invalid configuration: %v", err)
			}
		})
	}
}

func TestRuleReadFailureIsNotANonMatch(t *testing.T) {
	readErr := errors.New("rules unavailable")
	s := &configTestStore{rulesErr: readErr}
	ic := New(s.open(t))
	if _, err := ic.IsToolEnabled("Bash"); !errors.Is(err, readErr) {
		t.Fatalf("tool-enabled check hid rule read failure: %v", err)
	}
	if _, matched, err := ic.Match("Bash", []byte(`{}`)); matched || !errors.Is(err, readErr) {
		t.Fatalf("rule read failure became a non-match: matched=%v err=%v", matched, err)
	}
}

func TestJudgeConfigReadFailureDoesNotUseModelFailAction(t *testing.T) {
	for _, key := range []string{settingJudgeEnabled, settingJudgeProfileID, settingJudgePrompt, settingJudgeTimeoutSecs, settingJudgeFailAction, settingJudgeAskTimeoutSecs, settingJudgeAskTimeoutAction} {
		t.Run(key, func(t *testing.T) {
			readErr := errors.New("setting unavailable")
			s := &configTestStore{settings: map[string]string{settingJudgeEnabled: "true", settingJudgeFailAction: "allow"}, settingErrs: map[string]error{key: readErr}}
			ic := New(s.open(t))
			called := false
			ic.SetReviewer(func(context.Context, int64, string, ReviewInput) (Decision, error) {
				called = true
				return Decision{}, errors.New("local model failure")
			})
			dec, judged, err := ic.Judge(t.Context(), "Bash", []byte(`{}`))
			if called || !errors.Is(err, readErr) || judged || dec.Action != "" {
				t.Fatalf("config read failure was not propagated separately: called=%v judged=%v action=%q err=%v", called, judged, dec.Action, err)
			}
			if _, err := ic.GetJudgeConfig(); !errors.Is(err, readErr) {
				t.Fatalf("config GET hid read failure: %v", err)
			}
		})
	}
}

func TestJudgeDisabledAndDefaults(t *testing.T) {
	for _, settings := range []map[string]string{nil, {settingJudgeEnabled: "false"}} {
		ic := New((&configTestStore{settings: settings}).open(t))
		ic.SetReviewer(func(context.Context, int64, string, ReviewInput) (Decision, error) {
			t.Fatal("disabled judge was called")
			return Decision{}, nil
		})
		cfg, err := ic.GetJudgeConfig()
		if err != nil || cfg.Enabled || cfg.FailAction != "deny" || cfg.Prompt != DefaultJudgePrompt || cfg.TimeoutSeconds != defaultJudgeTimeoutSecs || cfg.AskTimeoutSeconds != defaultJudgeAskTimeoutSecs || cfg.AskTimeoutAction != defaultJudgeAskTimeoutAction {
			t.Fatalf("defaults changed: %+v err=%v", cfg, err)
		}
		if _, judged, err := ic.Judge(t.Context(), "Bash", []byte(`{}`)); judged || err != nil {
			t.Fatalf("explicitly disabled or unset judge did not pass through: judged=%v err=%v", judged, err)
		}
	}
}

func TestJudgeDisabledSkipsUnrelatedConfigReads(t *testing.T) {
	s := &configTestStore{
		settings:    map[string]string{settingJudgeEnabled: "false"},
		settingErrs: map[string]error{settingJudgePrompt: errors.New("prompt unavailable")},
	}
	ic := New(s.open(t))
	ic.SetReviewer(func(context.Context, int64, string, ReviewInput) (Decision, error) {
		t.Fatal("explicitly disabled judge was called")
		return Decision{}, nil
	})
	if _, judged, err := ic.Judge(t.Context(), "Bash", []byte(`{}`)); judged || err != nil {
		t.Fatalf("unrelated config failure blocked an explicitly disabled judge: judged=%v err=%v", judged, err)
	}
	if s.settingReads != 1 {
		t.Fatalf("disabled judge read unrelated settings: reads=%d", s.settingReads)
	}
	if _, err := ic.GetJudgeConfig(); err == nil {
		t.Fatal("config GET should still report errors reading the complete configuration")
	}
}

func TestDisabledToolSkipsRuleReads(t *testing.T) {
	for _, tools := range []string{`[]`, `["Write"]`} {
		t.Run(tools, func(t *testing.T) {
			s := &configTestStore{
				settings: map[string]string{"intercept_enabled_tools": tools},
				rulesErr: errors.New("rules unavailable"),
			}
			ic := New(s.open(t))
			if enabled, err := ic.IsToolEnabled("Bash"); enabled || err != nil {
				t.Fatalf("rules failure blocked an explicitly disabled tool: enabled=%v err=%v", enabled, err)
			}
			if s.rulesReads != 0 || ic.cached != nil || ic.enabledTools != nil {
				t.Fatalf("disabled-tool check read rules or published incomplete cache: reads=%d", s.rulesReads)
			}
		})
	}
}

func TestJudgeModelFailureUsesConfiguredAction(t *testing.T) {
	for _, action := range []string{"allow", "ask", "deny"} {
		for _, failure := range []string{"error", "invalid_verdict"} {
			t.Run(action+"/"+failure, func(t *testing.T) {
				s := &configTestStore{settings: map[string]string{settingJudgeEnabled: "true", settingJudgeFailAction: action, settingJudgeAskTimeoutSecs: "7", settingJudgeAskTimeoutAction: "allow"}}
				ic := New(s.open(t))
				ic.SetReviewer(func(context.Context, int64, string, ReviewInput) (Decision, error) {
					if failure == "error" {
						return Decision{}, errors.New("local model failure")
					}
					return Decision{}, nil
				})
				dec, judged, err := ic.Judge(t.Context(), "Bash", []byte(`{}`))
				if err != nil || !judged || dec.Action != action || !dec.ModelFallback {
					t.Fatalf("model failure policy changed: %+v judged=%v err=%v", dec, judged, err)
				}
				if action == "ask" && (!dec.TimeoutEnabled || dec.TimeoutSeconds != 7 || dec.TimeoutAction != "allow") {
					t.Fatalf("ask failure policy lost its configured timeout: %+v", dec)
				}
			})
		}
	}
}

func TestJudgeUnconfiguredFailureDoesNotApprove(t *testing.T) {
	for _, failure := range []string{"missing_reviewer", "model_error", "invalid_verdict"} {
		t.Run(failure, func(t *testing.T) {
			ic := New((&configTestStore{settings: map[string]string{settingJudgeEnabled: "true"}}).open(t))
			if failure != "missing_reviewer" {
				ic.SetReviewer(func(context.Context, int64, string, ReviewInput) (Decision, error) {
					if failure == "model_error" {
						return Decision{}, errors.New("fixture provider unavailable")
					}
					return Decision{}, nil
				})
			}
			decision, judged, err := ic.Judge(t.Context(), "Bash", []byte(`{}`))
			if err != nil || !judged || decision.Action != "deny" || !decision.ModelFallback {
				t.Fatalf("unreviewed request escaped default denial: %+v judged=%v err=%v", decision, judged, err)
			}
		})
	}
}

func TestTaskIDFromContextPreservesOrigin(t *testing.T) {
	if TaskIDFromContext(t.Context()) != "" {
		t.Fatal("platform context acquired a task")
	}
	ctx := WithTaskContext(t.Context(), "17", "fixture", nil)
	if TaskIDFromContext(ctx) != "17" {
		t.Fatal("task context was lost")
	}
}
