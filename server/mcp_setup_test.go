package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func setupTestMCP(t *testing.T, pg *db.DB, row *db.MCPServer) *db.MCPServer {
	t.Helper()
	row.Name = fmt.Sprintf("setup-%d", time.Now().UnixNano())
	if _, err := pg.SaveMCP(row); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pg.DeleteMCP(row.ID); err != nil {
			t.Error(err)
		}
	})
	approved, err := pg.SetMCPApproval(row.ID, row.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	return approved
}

func TestMCPSetupBoundsUnresponsiveProcess(t *testing.T) {
	for _, mode := range []string{"deadline", "parent_cancel"} {
		t.Run(mode, func(t *testing.T) {
			pg := mcpPolicyServer(t).m.pg
			sh, err := exec.LookPath("sh")
			if err != nil {
				t.Skip("real sh executable required")
			}
			sleep, err := exec.LookPath("sleep")
			if err != nil {
				t.Skip("real sleep executable required")
			}
			pidFile := filepath.Join(t.TempDir(), "pid")
			args, _ := json.Marshal([]string{"-c", `echo $$ > "$1"; exec "$2" 30`, "mcp-setup", pidFile, sleep})
			row := setupTestMCP(t, pg, &db.MCPServer{Transport: "stdio", Command: sh, Args: args})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			budget := 500 * time.Millisecond
			if mode == "parent_cancel" {
				budget = 10 * time.Second
			}
			done := make(chan error, 1)
			start := time.Now()
			go func() {
				cl, tools, err := setupAuthorizedMCP(ctx, pg, row.ID, row.Revision, budget)
				if cl != nil {
					cl.Close()
				}
				if cl != nil || len(tools) != 0 {
					done <- fmt.Errorf("unresponsive server returned a client or tools")
					return
				}
				done <- err
			}()
			if mode == "parent_cancel" {
				wait := time.NewTimer(3 * time.Second)
				defer wait.Stop()
				for {
					if _, err := os.Stat(pidFile); err == nil {
						break
					}
					select {
					case err := <-done:
						t.Fatalf("process did not start: %v", err)
					case <-wait.C:
						cancel()
						<-done
						t.Fatal("process startup did not produce PID")
					case <-time.After(10 * time.Millisecond):
					}
				}
				cancel()
			}
			select {
			case err := <-done:
				want := context.DeadlineExceeded
				if mode == "parent_cancel" {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Fatalf("setup error = %v, want %v", err, want)
				}
			case <-time.After(5 * time.Second):
				cancel()
				<-done
				t.Fatal("setup did not release on cancellation")
			}
			pidBytes, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
			if err != nil {
				t.Fatal(err)
			}
			proc, err := os.FindProcess(pid)
			if err == nil && proc.Signal(syscall.Signal(0)) == nil {
				proc.Kill()
				t.Fatalf("owned MCP process %d survived failed setup", pid)
			}
			t.Logf("setup returned and owned process exited after %s", time.Since(start).Round(time.Millisecond))
		})
	}
}

// 官方 MCP SDK 可执行文件由隔离验证环境提供，正常测试不下载或构造协议替身。
func TestMCPSetupOfficialServerSurvivesSetupCancellation(t *testing.T) {
	for _, transport := range []string{"stdio", "sse"} {
		t.Run(transport, func(t *testing.T) {
			row := &db.MCPServer{Transport: transport}
			toolName := "greet"
			if transport == "stdio" {
				row.Command = os.Getenv("ARTEX_MCP_SETUP_STDIO_COMMAND")
				if row.Command == "" {
					t.Skip("official SDK stdio executable not configured")
				}
			} else {
				row.URL = os.Getenv("ARTEX_MCP_SETUP_SSE_URL")
				toolName = "greet1"
				if row.URL == "" {
					t.Skip("official SDK SSE service not configured")
				}
			}
			pg := mcpPolicyServer(t).m.pg
			command := row.Command
			commandPath := ""
			if transport == "stdio" {
				commandPath = filepath.Join(t.TempDir(), "official-server")
				row.Command = commandPath
			}
			row = setupTestMCP(t, pg, row)
			if transport == "stdio" {
				cl, _, err := setupAuthorizedMCP(context.Background(), pg, row.ID, row.Revision, 3*time.Second)
				if err == nil || cl != nil {
					if cl != nil {
						cl.Close()
					}
					t.Fatal("missing executable did not report unavailable")
				}
				if err := os.Symlink(command, commandPath); err != nil {
					t.Fatal(err)
				}
			}
			cl, tools, err := setupAuthorizedMCP(context.Background(), pg, row.ID, row.Revision, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer cl.Close()
			for _, tool := range tools {
				if !strings.HasSuffix(tool.Name(), "__"+toolName) {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				result, err := tool.Call(ctx, json.RawMessage(`{"name":"setup-cancel"}`), nil)
				if err != nil || result.IsError || !strings.Contains(result.Flatten(), "Hi setup-cancel") {
					t.Fatalf("healthy tool after setup context cancellation: %v, %s", err, result.Flatten())
				}
				return
			}
			t.Fatalf("official tool %s missing", toolName)
		})
	}
}
