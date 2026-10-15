package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func mcpPolicyDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_MCP_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("ARTEX_PG_DSN")
	}
	if dsn == "" {
		t.Skip("explicit isolated ARTEX_MCP_TEST_DSN required")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}
func TestMCPRevisionApprovalLifecycle(t *testing.T) {
	d := mcpPolicyDB(t)
	for _, transport := range []string{"stdio", "http", "sse"} {
		t.Run(transport, func(t *testing.T) {
			m := &MCPServer{Name: fmt.Sprintf("r19-%s-%d", transport, time.Now().UnixNano()), Transport: transport, Command: "never-executed", Args: json.RawMessage(`["with spaces"]`), Env: json.RawMessage(`{"TOKEN":"fixture","A":"1"}`), URL: "http://127.0.0.1:1", Enabled: true, Approved: true, ApprovedRevision: 1}
			if _, err := d.SaveMCP(m); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { d.DeleteMCP(m.ID) })
			if m.Enabled || m.Approved {
				t.Fatal("save granted authority")
			}
			if _, err := d.AuthorizedMCP(m.ID, m.Revision); err == nil {
				t.Fatal("draft admitted")
			}
			draft := *m
			approved, err := d.SetMCPApproval(m.ID, m.Revision, true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.SaveMCP(&draft); err != ErrMCPRevision {
				t.Fatalf("stale save: %v", err)
			}
			if _, err = d.SetMCPApproval(m.ID, draft.Revision, true); err != ErrMCPRevision {
				t.Fatalf("stale approval: %v", err)
			}
			m, err = d.AuthorizedMCP(m.ID, approved.Revision)
			if err != nil {
				t.Fatal(err)
			}
			old := m.Revision
			m.Env = json.RawMessage(`{"A":"1","TOKEN":"fixture"}`)
			if _, err = d.SaveMCP(m); err != nil {
				t.Fatal(err)
			}
			if !m.Approved || !m.Enabled || m.Revision != old {
				t.Fatal("canonical same save lost approval")
			}
			m.Env = nil
			m.Enabled = false
			if _, err = d.SaveMCP(m); err != nil {
				t.Fatal(err)
			}
			current, err := d.AuthorizedMCP(m.ID, m.Revision)
			if err != nil || string(current.Env) == "{}" {
				t.Fatal("omitted credentials erased or ordinary enabled field mutated authority")
			}
			if err = d.SaveMCPTools(m.ID, []MCPTool{{Name: "fixture"}}, m.Revision); err != nil {
				t.Fatal(err)
			}
			disabled, err := d.SetMCPApproval(m.ID, m.Revision, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.AuthorizedMCP(m.ID, old); err == nil {
				t.Fatal("old client admitted")
			}
			if err = d.SaveMCPTools(m.ID, []MCPTool{{Name: "stale"}}, old); err == nil {
				t.Fatal("stale cache accepted")
			}
			m.Revision = disabled.Revision
			m.Enabled = true
			if _, err = d.SaveMCP(m); err != nil {
				t.Fatal(err)
			}
			if m.Enabled || m.Approved {
				t.Fatal("model restored disabled authority")
			}
			approved, err = d.SetMCPApproval(m.ID, m.Revision, true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.AuthorizedMCP(m.ID, old); err == nil {
				t.Fatal("old client revived after reapproval")
			}
			m, err = d.AuthorizedMCP(m.ID, approved.Revision)
			if err != nil {
				t.Fatal(err)
			}
			m.Env = json.RawMessage(`{}`)
			if _, err = d.SaveMCP(m); err != nil {
				t.Fatal(err)
			}
			if m.Enabled || m.Approved {
				t.Fatal("credential edit retained authority")
			}
			if _, err = d.AuthorizedMCP(m.ID, m.Revision); err == nil {
				t.Fatal("changed draft admitted")
			}
		})
	}
}
func TestMCPSecurityFieldsRevoke(t *testing.T) {
	d := mcpPolicyDB(t)
	changes := map[string]func(*MCPServer){"name": func(m *MCPServer) { m.Name += "x" }, "transport": func(m *MCPServer) { m.Transport = "sse" }, "command": func(m *MCPServer) { m.Command = "other" }, "args": func(m *MCPServer) { m.Args = json.RawMessage(`["new"]`) }, "env": func(m *MCPServer) { m.Env = json.RawMessage(`{"TOKEN":"new"}`) }, "url": func(m *MCPServer) { m.URL += "/new" }, "insecure": func(m *MCPServer) { m.Insecure = true }}
	for field, change := range changes {
		t.Run(field, func(t *testing.T) {
			m := &MCPServer{Name: fmt.Sprintf("r19-fields-%s-%d", field, time.Now().UnixNano()), Transport: "http", URL: "http://127.0.0.1:1"}
			if _, err := d.SaveMCP(m); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { d.DeleteMCP(m.ID) })
			a, err := d.SetMCPApproval(m.ID, m.Revision, true)
			if err != nil {
				t.Fatal(err)
			}
			m, err = d.AuthorizedMCP(m.ID, a.Revision)
			if err != nil {
				t.Fatal(err)
			}
			old := m.Revision
			change(m)
			if _, err = d.SaveMCP(m); err != nil {
				t.Fatal(err)
			}
			if m.Revision <= old || m.Approved || m.Enabled {
				t.Fatal("changed config retained authority")
			}
		})
	}
}

func TestMCPLegacyMigrationPreservesDataWithoutApproval(t *testing.T) {
	d := mcpPolicyDB(t)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	schema := fmt.Sprintf("r19_legacy_%d", time.Now().UnixNano())
	if _, err = tx.Exec(`CREATE SCHEMA ` + schema + `; SET LOCAL search_path TO ` + schema); err != nil {
		t.Fatal(err)
	}
	// A pre-R19 table, with a formerly enabled local endpoint and visibility.
	_, err = tx.Exec(`CREATE TABLE mcp_servers(id BIGSERIAL PRIMARY KEY,name TEXT NOT NULL UNIQUE,transport TEXT NOT NULL,command TEXT,args JSONB NOT NULL DEFAULT '[]',env JSONB NOT NULL DEFAULT '{}',url TEXT,enabled BOOLEAN NOT NULL DEFAULT true,insecure BOOLEAN NOT NULL DEFAULT false,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
 CREATE TABLE agent_visibility(agent_id BIGINT NOT NULL,resource_kind TEXT NOT NULL,resource_id BIGINT NOT NULL,mcp_tool_name TEXT NOT NULL DEFAULT '',enabled BOOLEAN NOT NULL DEFAULT true,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(agent_id,resource_kind,resource_id,mcp_tool_name));
 INSERT INTO mcp_servers(name,transport,args,env,url,enabled) VALUES('legacy','http','["with spaces"]','{"TOKEN":"legacy-secret"}','http://127.0.0.1:1234',true);
 INSERT INTO agent_visibility(agent_id,resource_kind,resource_id) VALUES(42,'mcp',1);`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	var revision, approved int64
	var enabled, visible bool
	var env, args, url string
	err = tx.QueryRow(`SELECT revision,approved_revision,enabled,env::text,args::text,url FROM mcp_servers WHERE name='legacy'`).Scan(&revision, &approved, &enabled, &env, &args, &url)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 1 || approved != 0 || !enabled || !strings.Contains(env, "legacy-secret") || args != `["with spaces"]` || url != "http://127.0.0.1:1234" {
		t.Fatal("legacy config lost or grandfathered")
	}
	if err = tx.QueryRow(`SELECT enabled FROM agent_visibility WHERE agent_id=42 AND resource_id=1`).Scan(&visible); err != nil || !visible {
		t.Fatal("legacy visibility lost")
	}
	if _, err = tx.Exec(`UPDATE mcp_servers SET revision=2,approved_revision=2 WHERE name='legacy'`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(`SELECT approved_revision FROM mcp_servers WHERE name='legacy'`).Scan(&approved); err != nil || approved != 2 {
		t.Fatal("restart erased explicit approval")
	}
}
func TestMCPConcurrentApprovalAndChange(t *testing.T) {
	d := mcpPolicyDB(t)
	m := &MCPServer{Name: fmt.Sprintf("r19-race-%d", time.Now().UnixNano()), Transport: "http", URL: "http://127.0.0.1:1"}
	if _, err := d.SaveMCP(m); err != nil {
		t.Fatal(err)
	}
	defer d.DeleteMCP(m.ID)
	revision := m.Revision
	m.URL += "/changed"
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; _, err := d.SaveMCP(m); results <- err }()
	go func() { <-start; _, err := d.SetMCPApproval(m.ID, revision, true); results <- err }()
	close(start)
	success := 0
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			success++
		} else if err != ErrMCPRevision {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal("competing epochs both committed")
	}
	var endpoint string
	var enabled bool
	var rev, approved int64
	if err := d.QueryRow(`SELECT url,enabled,revision,approved_revision FROM mcp_servers WHERE id=$1`, m.ID).Scan(&endpoint, &enabled, &rev, &approved); err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(endpoint, "/changed") && (enabled || approved != 0) {
		t.Fatal("change acquired competing approval")
	}
	if rev != revision+1 {
		t.Fatal("epoch did not advance")
	}
}

func TestMCPAuthorizationCancellationWhenPoolExhausted(t *testing.T) {
	d := mcpPolicyDB(t)
	d.SetMaxOpenConns(1)
	conn, err := d.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = d.AuthorizedMCPContext(ctx, 1, 1)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("authorization ignored cancellation: %v", err)
	}
}
func TestMCPApprovalCancellationWhileRowLocked(t *testing.T) {
	d := mcpPolicyDB(t)
	m := &MCPServer{Name: fmt.Sprintf("r19-lock-%d", time.Now().UnixNano()), Transport: "http"}
	if _, err := d.SaveMCP(m); err != nil {
		t.Fatal(err)
	}
	defer d.DeleteMCP(m.ID)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var id int64
	if err = tx.QueryRow(`SELECT id FROM mcp_servers WHERE id=$1 FOR UPDATE`, m.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = d.SetMCPApprovalContext(ctx, m.ID, m.Revision, true)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("approval ignored cancellation: %v", err)
	}
	tx.Rollback()
	if _, err = d.AuthorizedMCP(m.ID, m.Revision); err == nil {
		t.Fatal("cancelled approval committed")
	}
}
func TestMCPInvalidShapesDoNotEchoSecrets(t *testing.T) {
	for _, mutate := range []func(*MCPServer){func(m *MCPServer) { m.Transport = "secret-value" }, func(m *MCPServer) { m.URL = "file:///secret-value" }, func(m *MCPServer) { m.Args = json.RawMessage(`{"secret-value":true}`) }, func(m *MCPServer) { m.Env = json.RawMessage(`{"TOKEN":{"secret-value":true}}`) }} {
		m := &MCPServer{Name: "fixture", Transport: "http"}
		mutate(m)
		err := validateMCP(m)
		if !errors.Is(err, ErrMCPInvalid) || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("invalid input leaked or accepted: %v", err)
		}
	}
}

func TestMCPStringCollectionsRejectNullMembers(t *testing.T) {
	for _, tc := range []struct {
		name, args, env string
		valid           bool
	}{
		{"null argument", `[null]`, `{}`, false},
		{"mixed null argument", `["keep",null]`, `{}`, false},
		{"null env value", `[]`, `{"TOKEN":null}`, false},
		{"mixed null env", `[]`, `{"A":"keep","TOKEN":null}`, false},
		{"numeric argument", `[1]`, `{}`, false},
		{"boolean env", `[]`, `{"TOKEN":false}`, false},
		{"empty collections", `[]`, `{}`, true},
		{"empty string values", `[""]`, `{"TOKEN":""}`, true},
		{"normal strings", `["with spaces"]`, `{"TOKEN":"fixture"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &MCPServer{Name: "fixture", Transport: "http", Args: json.RawMessage(tc.args), Env: json.RawMessage(tc.env)}
			err := validateMCP(m)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
		})
	}
}
func TestMCPNullMemberSavePreservesApprovedCredentials(t *testing.T) {
	d := mcpPolicyDB(t)
	m := &MCPServer{Name: fmt.Sprintf("r19-null-%d", time.Now().UnixNano()), Transport: "http", Args: json.RawMessage(`["keep"]`), Env: json.RawMessage(`{"TOKEN":"fixture-secret"}`)}
	if _, err := d.SaveMCP(m); err != nil {
		t.Fatal(err)
	}
	defer d.DeleteMCP(m.ID)
	a, err := d.SetMCPApproval(m.ID, m.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	original, err := d.AuthorizedMCP(m.ID, a.Revision)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"args", "env"} {
		bad := *original
		if field == "args" {
			bad.Args = json.RawMessage(`[null]`)
		} else {
			bad.Env = json.RawMessage(`{"TOKEN":null}`)
		}
		if _, err = d.SaveMCP(&bad); !errors.Is(err, ErrMCPInvalid) {
			t.Fatalf("%s null accepted: %v", field, err)
		}
		persisted, err := d.AuthorizedMCP(original.ID, original.Revision)
		if err != nil {
			t.Fatalf("invalid input changed approval: %v", err)
		}
		if string(persisted.Args) != string(original.Args) || string(persisted.Env) != string(original.Env) || persisted.Revision != original.Revision {
			t.Fatal("invalid input changed stored arguments or credentials")
		}
	}
}
