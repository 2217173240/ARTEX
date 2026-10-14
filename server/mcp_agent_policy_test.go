package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestAgentMCPStdioRequiresOperatorEnable(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("explicit isolated ARTEX_PG_DSN required")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := &Server{m: m}
	name := fmt.Sprintf("mcp-policy-%d", time.Now().UnixNano())
	input := mcpToolInput{Name: name, Transport: " STDIO ", Command: "not-executed", Args: json.RawMessage(`["literal; not a shell"]`)}
	enabled := true
	input.Enabled = &enabled
	call := func(update bool) string {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		tool := s.toolCreateMCP()
		if update {
			tool = s.toolUpdateMCP()
		}
		result, err := tool.Call(context.Background(), body, nil)
		if err != nil || result.IsError {
			t.Fatalf("platform write failed: %v %s", err, result.Flatten())
		}
		return result.Flatten()
	}
	find := func() *db.MCPServer {
		t.Helper()
		rows, err := m.pg.ListMCP()
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Name == name {
				return row
			}
		}
		t.Fatal("saved MCP not found")
		return nil
	}
	if message := call(false); !strings.Contains(message, "stdio disabled") {
		t.Fatal("create result hid the disabled state")
	}
	row := find()
	defer m.pg.DeleteMCP(row.ID)
	if row.Enabled || row.Transport != "stdio" || row.Command != input.Command || string(row.Args) != string(input.Args) {
		t.Fatal("agent configuration unexpectedly gained execution or lost its argv")
	}
	// The operator handler keeps its explicit enable capability. Use an update
	// so this synthetic test never starts a subprocess for initial discovery.
	row.Enabled = true
	request, _ := json.Marshal(row)
	w := httptest.NewRecorder()
	s.pgSaveMCP(w, httptest.NewRequest("POST", "/api/mcp", strings.NewReader(string(request))))
	if w.Code != 200 || !find().Enabled {
		t.Fatal("operator management path lost explicit enable")
	}
	input.ID = row.ID
	input.Command = "changed-but-not-executed"
	if message := call(true); !strings.Contains(message, "stdio disabled") || find().Enabled {
		t.Fatal("agent rewrite retained executable approval")
	}
	// Remote configuration remains usable through the existing platform API.
	input.Transport, input.URL = "http", "http://127.0.0.1:1/not-contacted"
	if message := call(true); strings.Contains(message, "stdio disabled") || !find().Enabled {
		t.Fatal("stdio policy unexpectedly disabled remote MCP")
	}
}

func TestAgentMCPStorageFailureReportsError(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("explicit isolated ARTEX_PG_DSN required")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.pg.Close()
	result, err := (&Server{m: m}).toolCreateMCP().Call(context.Background(), json.RawMessage(`{"name":"unpersisted","transport":"stdio","command":"not-executed","enabled":true}`), nil)
	if err != nil || !result.IsError || strings.Contains(result.Flatten(), "mcp created") {
		t.Fatal("failed save was reported as a usable configuration")
	}
}
