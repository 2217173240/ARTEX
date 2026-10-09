package guard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// This opt-in regression uses only session-local temporary tables on an
// explicitly selected local test DB. It never resolves the application DSN or
// changes public tables, even if the supplied database already has a schema.
func interceptConfigTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_INTERCEPT_TEST_DSN")
	if dsn == "" {
		t.Skip("set ARTEX_INTERCEPT_TEST_DSN to an isolated local PostgreSQL database")
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.Ping(); err != nil {
		t.Fatal(err)
	}
	d := &db.DB{DB: conn}
	if _, err := d.Exec(`SET search_path TO pg_temp`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`CREATE TEMP TABLE intercept_rules (
		id bigint PRIMARY KEY, name text NOT NULL, enabled boolean NOT NULL,
		priority integer NOT NULL, match_target text NOT NULL, match_type text NOT NULL,
		pattern text NOT NULL, action text NOT NULL, message text NOT NULL,
		timeout_enabled boolean NOT NULL DEFAULT false, timeout_seconds integer NOT NULL DEFAULT 0,
		timeout_action text NOT NULL DEFAULT 'deny', created_at timestamptz NOT NULL DEFAULT now(),
		updated_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		t.Fatal(err)
	}
	createInterceptTestSettings(t, d)
	return d
}

func createInterceptTestSettings(t *testing.T, d *db.DB) {
	t.Helper()
	if _, err := d.Exec(`CREATE TEMP TABLE settings (key text PRIMARY KEY, value text NOT NULL, updated_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
}

func configureInterceptTest(t *testing.T, d *db.DB, tools string, judgeEnabled bool, failAction string) {
	t.Helper()
	if err := d.SetSetting("intercept_enabled_tools", tools); err != nil {
		t.Fatal(err)
	}
	if err := d.SetBool("llm_judge_enabled", judgeEnabled); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting("llm_judge_fail_action", failAction); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresInterceptConfigFailures(t *testing.T) {
	t.Run("rules_success_settings_failure", func(t *testing.T) {
		d := interceptConfigTestDB(t)
		if _, err := d.Exec(`INSERT INTO intercept_rules(id,name,enabled,priority,match_target,match_type,pattern,action,message)
			VALUES (1,'local probe',true,1,'tool_name','contains','Bash','allow','local rule')`); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`DROP TABLE pg_temp.settings`); err != nil {
			t.Fatal(err)
		}
		ic := intercept.New(d)
		g := NewWithInterceptor(ic)
		blocked, message, _ := g.Hooks().PreToolUse(t.Context(), "Bash", json.RawMessage(`{}`))
		if !blocked || !strings.Contains(message, "配置读取失败") {
			t.Fatalf("partial configuration permitted execution: blocked=%v message=%q", blocked, message)
		}
		if _, _, err := ic.Match("Bash", []byte(`{}`)); err == nil {
			t.Fatal("failed load left rules published without enabled-tool configuration")
		}
		createInterceptTestSettings(t, d)
		configureInterceptTest(t, d, `[]`, false, "allow")
		blocked, _, _ = g.Hooks().PreToolUse(t.Context(), "Bash", json.RawMessage(`{}`))
		if blocked {
			t.Fatal("explicitly disabled tools did not recover after configuration storage was restored")
		}
	})
	t.Run("judge_enabled_read_failure", func(t *testing.T) {
		d := interceptConfigTestDB(t)
		configureInterceptTest(t, d, `["Bash"]`, true, "allow")
		ic := intercept.New(d)
		called := false
		ic.SetReviewer(func(context.Context, int64, string, intercept.ReviewInput) (intercept.Decision, error) {
			called = true
			return intercept.Decision{}, errors.New("local model failure")
		})
		if enabled, err := ic.IsToolEnabled("Bash"); !enabled || err != nil {
			t.Fatalf("could not populate valid tool/rule cache: enabled=%v err=%v", enabled, err)
		}
		if _, err := d.Exec(`DROP TABLE pg_temp.settings`); err != nil {
			t.Fatal(err)
		}
		g := NewWithInterceptor(ic)
		blocked, message, _ := g.Hooks().PreToolUse(t.Context(), "Bash", json.RawMessage(`{}`))
		if called || !blocked || !strings.Contains(message, "模型审批配置读取失败") {
			t.Fatalf("judge storage failure became disabled/model allow: called=%v blocked=%v message=%q", called, blocked, message)
		}
	})
}

func TestPostgresInterceptConfiguredPassthrough(t *testing.T) {
	for _, tc := range []struct {
		name, tools string
		judge       bool
		modelCalled bool
	}{
		{name: "explicit_tools_disabled", tools: `[]`, judge: true},
		{name: "unmatched_rule_judge_disabled", tools: `["Bash"]`},
		{name: "explicit_model_failure_allow", tools: `["Bash"]`, judge: true, modelCalled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := interceptConfigTestDB(t)
			configureInterceptTest(t, d, tc.tools, tc.judge, "allow")
			if _, err := d.Exec(`INSERT INTO intercept_rules(id,name,enabled,priority,match_target,match_type,pattern,action,message)
				VALUES (1,'unmatched local probe',true,1,'tool_name','contains','OtherTool','deny','local rule')`); err != nil {
				t.Fatal(err)
			}
			ic := intercept.New(d)
			called := false
			ic.SetReviewer(func(context.Context, int64, string, intercept.ReviewInput) (intercept.Decision, error) {
				called = true
				return intercept.Decision{}, errors.New("local model failure")
			})
			blocked, message, _ := NewWithInterceptor(ic).Hooks().PreToolUse(t.Context(), "Bash", json.RawMessage(`{}`))
			if blocked || called != tc.modelCalled {
				t.Fatalf("configured passthrough changed: called=%v blocked=%v message=%q", called, blocked, message)
			}
		})
	}
}
