package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func mcpPolicyServer(t *testing.T) *Server {
	t.Helper()
	dsn := os.Getenv("ARTEX_MCP_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("ARTEX_PG_DSN")
	}
	if dsn == "" {
		t.Skip("explicit isolated ARTEX_MCP_TEST_DSN required")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close() })
	return &Server{m: &Manager{pg: pg}}
}
func TestAgentMCPAllTransportsRequireApproval(t *testing.T) {
	s := mcpPolicyServer(t)
	for _, transport := range []string{"stdio", "http", "sse"} {
		t.Run(transport, func(t *testing.T) {
			in := map[string]any{"name": fmt.Sprintf("r19-agent-%s-%d", transport, time.Now().UnixNano()), "transport": transport, "command": "never-executed", "url": "http://127.0.0.1:1", "enabled": true, "env": map[string]string{"TOKEN": "fixture-secret"}}
			b, _ := json.Marshal(in)
			r, e := s.toolCreateMCP().Call(context.Background(), b, nil)
			if e != nil || r.IsError {
				t.Fatalf("create %v %s", e, r.Flatten())
			}
			rows, _ := s.m.pg.ListMCP()
			var row *db.MCPServer
			for _, m := range rows {
				if m.Name == in["name"] {
					row = m
				}
			}
			if row == nil || row.Enabled || row.Approved {
				t.Fatal("agent self-authorized")
			}
			t.Cleanup(func() { s.m.pg.DeleteMCP(row.ID) })
			// Ordinary operator save also cannot authorize and must not discover stdio.
			row.Enabled = true
			b, _ = json.Marshal(row)
			w := httptest.NewRecorder()
			s.pgSaveMCP(w, httptest.NewRequest("POST", "/api/mcp", strings.NewReader(string(b))))
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			if _, err := s.m.pg.AuthorizedMCP(row.ID, row.Revision); err == nil {
				t.Fatal("save authorized")
			}
			w = httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/mcp/1/refresh", nil)
			req.SetPathValue("id", fmt.Sprint(row.ID))
			s.pgRefreshMCP(w, req)
			if w.Code == 200 {
				t.Fatal("draft refresh allowed")
			}
		})
	}
	w := httptest.NewRecorder()
	s.pgListMCP(w, httptest.NewRequest("GET", "/api/mcp", nil))
	if strings.Contains(w.Body.String(), "fixture-secret") || strings.Contains(w.Body.String(), `"env":`) {
		t.Fatal("GET leaked credential")
	}
	r, e := s.toolListMCP().Call(context.Background(), json.RawMessage(`{}`), nil)
	if e != nil || r.IsError || strings.Contains(r.Flatten(), "fixture-secret") {
		t.Fatal("list_mcp leak/error")
	}
}
func TestMCPLiveClientRevocationAndScopeSentry(t *testing.T) {
	s := mcpPolicyServer(t)
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "fixture", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			calls.Add(1)
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer target.Close()
	m := &db.MCPServer{Name: fmt.Sprintf("r19-live-%d", time.Now().UnixNano()), Transport: "http", URL: target.URL}
	s.m.pg.SaveMCP(m)
	t.Cleanup(func() { s.m.pg.DeleteMCP(m.ID) })
	if _, err := connectAuthorizedMCP(context.Background(), s.m.pg, m.ID, m.Revision); err == nil {
		t.Fatal("draft dial")
	}
	a, err := s.m.pg.SetMCPApproval(m.ID, m.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	cl, err := connectAuthorizedMCP(context.Background(), s.m.pg, m.ID, a.Revision)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ts, err := cl.Tools(context.Background())
	if err != nil || len(ts) != 1 {
		t.Fatalf("tools %v %d", err, len(ts))
	}
	call := func(tool actool.CoreTool) error {
		_, err := tool.Call(context.Background(), json.RawMessage(`{}`), nil)
		return err
	}
	if err = call(ts[0]); err != nil {
		t.Fatal(err)
	}
	// An existing live tool must propagate cancellation while admission waits
	// for a DB connection, without reaching its already-connected HTTP target.
	s.m.pg.SetMaxOpenConns(1)
	held, err := s.m.pg.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	_, waitErr := ts[0].Call(waitCtx, json.RawMessage(`{}`), nil)
	cancel()
	held.Close()
	s.m.pg.SetMaxOpenConns(32)
	if !errors.Is(waitErr, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatalf("live tool admission ignored cancellation: %v", waitErr)
	}
	// Editing a live endpoint revokes existing clients, even after reapproval.
	edited, err := s.m.pg.AuthorizedMCP(m.ID, a.Revision)
	if err != nil {
		t.Fatal(err)
	}
	edited.URL = target.URL + "?changed"
	if _, err = s.m.pg.SaveMCP(edited); err != nil {
		t.Fatal(err)
	}
	if call(ts[0]) == nil || calls.Load() != 1 {
		t.Fatal("old live client survived URL edit")
	}
	a, err = s.m.pg.SetMCPApproval(m.ID, edited.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	if call(ts[0]) == nil {
		t.Fatal("old live client revived after edited endpoint reapproval")
	}
	disabled, err := s.m.pg.SetMCPApproval(m.ID, a.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	if call(ts[0]) == nil || calls.Load() != 1 {
		t.Fatal("existing tool survived disable")
	}
	a, err = s.m.pg.SetMCPApproval(m.ID, disabled.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	if call(ts[0]) == nil {
		t.Fatal("old client revived")
	}
	s.m.pg.DeleteMCP(m.ID)
	if call(ts[0]) == nil {
		t.Fatal("deleted server called")
	}
	// ScopeSentry native Call applies the same epoch check to every operation.
	m = &db.MCPServer{Name: fmt.Sprintf("r19-native-%d", time.Now().UnixNano()), Transport: "http", URL: target.URL}
	s.m.pg.SaveMCP(m)
	t.Cleanup(func() { s.m.pg.DeleteMCP(m.ID) })
	a, _ = s.m.pg.SetMCPApproval(m.ID, m.Revision, true)
	native := &authorizedScopeSentryClient{pg: s.m.pg, id: m.ID, revision: a.Revision}
	s.m.pg.SetMCPApproval(m.ID, a.Revision, false)
	if _, err = native.Call(context.Background(), "echo", nil); err == nil {
		t.Fatal("native call bypass")
	}
	s.m.pg.Close()
	if call(ts[0]) == nil {
		t.Fatal("DB failure admitted call")
	}
}

func TestMCPBrowserDerivedChangeCannotApprove(t *testing.T) {
	s := mcpPolicyServer(t)
	all, err := s.m.pg.ListMCP()
	if err != nil {
		t.Fatal(err)
	}
	var browser *db.MCPServer
	for _, m := range all {
		if m.Name == browserMCPName {
			browser = m
		}
	}
	if browser == nil {
		t.Fatal("missing seeded browser")
	}
	original := *browser
	t.Cleanup(func() {
		rows, _ := s.m.pg.ListMCP()
		for _, m := range rows {
			if m.ID == original.ID {
				original.Revision = m.Revision
				s.m.pg.SaveMCP(&original)
			}
		}
	})
	browser.Command = "never-executed"
	browser.Args = json.RawMessage(`["--proxy-server","http://127.0.0.1:1","with spaces"]`)
	browser.Env = json.RawMessage(`{"TOKEN":"fixture-secret","NODE_EXTRA_CA_CERTS":"fixture-path"}`)
	if _, err = s.m.pg.SaveMCP(browser); err != nil {
		t.Fatal(err)
	}
	approved, err := s.m.pg.SetMCPApproval(browser.ID, browser.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	browser.Revision = approved.Revision
	stale := *browser
	// Derived changes revoke even an approved configuration. A stale snapshot
	// cannot overwrite that new revision or its retained credentials.
	s.m.syncBrowserMCPProxy()
	stale.Env = json.RawMessage(`{"TOKEN":"stale-overwrite"}`)
	if _, err = s.m.pg.SaveMCP(&stale); err != db.ErrMCPRevision {
		t.Fatalf("stale browser overwrite: %v", err)
	}
	rows, err := s.m.pg.ListMCP()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rows {
		if m.ID != browser.ID {
			continue
		}
		if m.Approved || m.Enabled || m.Command != "never-executed" || !strings.Contains(string(m.Env), "fixture-secret") || strings.Contains(string(m.Env), "NODE_EXTRA_CA_CERTS") || string(m.Args) != `["with spaces"]` {
			t.Fatalf("browser sync lost configuration or granted authority: revision=%d", m.Revision)
		}
	}
}
func TestMCPModelInputCannotEnable(t *testing.T) {
	yes := true
	for _, transport := range []string{"stdio", "http", "sse"} {
		m := (mcpToolInput{Transport: transport, Enabled: &yes}).toDB()
		if m.Enabled || m.Approved {
			t.Fatal("model enabled " + transport)
		}
	}
}

func TestMCPApprovalRoutesRequireAuthentication(t *testing.T) {
	s, _ := newAuthMemoryServer(t)
	h := s.Handler()
	for _, action := range []string{"approve", "disable"} {
		for _, origin := range []string{"", "https://attacker.test"} {
			r := httptest.NewRequest("POST", "http://localhost:8787/api/mcp/1/"+action, strings.NewReader(`{"revision":1}`))
			if origin != "" {
				r.Header.Set("Origin", origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := http.StatusUnauthorized
			if origin != "" {
				want = http.StatusForbidden
			}
			if w.Code != want {
				t.Fatalf("%s anonymous status %d want %d", action, w.Code, want)
			}
		}
	}
}
func TestMCPHandlerStorageFailureIsRetryableAndSanitized(t *testing.T) {
	s := mcpPolicyServer(t)
	s.m.pg.Close()
	for _, action := range []string{"save", "approve", "disable"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/mcp/1/"+action, strings.NewReader(`{"name":"fixture-secret","transport":"http","revision":1,"env":{"TOKEN":"fixture-secret"}}`))
		r.SetPathValue("id", "1")
		switch action {
		case "save":
			s.pgSaveMCP(w, r)
		case "approve":
			s.pgApproveMCP(w, r)
		case "disable":
			s.pgDisableMCP(w, r)
		}
		if w.Code != 503 || strings.Contains(w.Body.String(), "fixture-secret") || strings.Contains(w.Body.String(), "database is closed") {
			t.Fatalf("%s failure response: %d %s", action, w.Code, w.Body.String())
		}
	}
}
