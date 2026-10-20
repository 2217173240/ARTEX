package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

var serverSecretCanaryKeys = []string{"ARTEX_PG_DSN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "ARTEX_LLM_BASE_URL", "ARTEX_LLM_PROXY"}

func setServerSecretCanaries(t *testing.T) {
	t.Helper()
	for _, key := range serverSecretCanaryKeys {
		t.Setenv(key, "server-env-canary")
	}
	t.Setenv("ARTEX_LLM_CALL_TIMEOUT", "37")
	t.Setenv("REQUESTS_CA_BUNDLE", "inherited-cert.pem")
	t.Setenv("TOOL_ENV_PRESERVED", "runtime-canary")
}

func TestCustomToolEnvironment(t *testing.T) {
	setServerSecretCanaries(t)
	interp := pythonFixture(t)
	work := t.TempDir()
	code := `import os, json
keys = ['ARTEX_PG_DSN','ANTHROPIC_API_KEY','ARTEX_LLM_BASE_URL','ARTEX_LLM_PROXY']
checks = [not os.getenv(k) for k in keys]
checks += [os.getenv('OPENAI_API_KEY', '') == os.getenv('TOOL_EXPECTED'), os.getenv('TOOL_TARGET') == 'fixture', os.getenv('ARTEX_LLM_CALL_TIMEOUT') == '37', os.getenv('REQUESTS_CA_BUNDLE') == os.getenv('TOOL_CERT'), os.getenv('TOOL_ENV_PRESERVED') == 'runtime-canary', bool(os.getenv('PATH')), bool(os.getenv('HOME'))]
print(json.dumps(checks))
`
	for _, explicit := range []bool{false, true} {
		var env []string
		expected := ""
		cert := "inherited-cert.pem"
		if explicit {
			expected = "explicit-canary"
			cert = "explicit-cert.pem"
			env = []string{"OPENAI_API_KEY=" + expected, "REQUESTS_CA_BUNDLE=" + cert}
		}
		out, err := execPython(context.Background(), interp, "env-policy", code, map[string]any{"target": "fixture", "expected": expected, "cert": cert}, work, env, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		var checks []bool
		if err := json.Unmarshal([]byte(out), &checks); err != nil || len(checks) != 11 {
			t.Fatalf("Python checks invalid: %v %s", err, out)
		}
		for i, ok := range checks {
			if !ok {
				t.Errorf("Python environment check %d failed (explicit=%t)", i, explicit)
			}
		}
		if runtime.GOOS != "windows" {
			command := `test -z "$ARTEX_PG_DSN" && test -z "$ANTHROPIC_API_KEY" && test -z "$ARTEX_LLM_BASE_URL" && test -z "$ARTEX_LLM_PROXY" && test "$OPENAI_API_KEY" = {expected} && test "$ARTEX_LLM_CALL_TIMEOUT" = 37 && printf command-ok`
			spec, _ := json.Marshal(commandExec{Command: command})
			tc := &actool.ToolContext{WorkingDir: work, Env: env}
			result, err := (&Server{}).runCommandTool(context.Background(), spec, map[string]any{"expected": expected}, tc)
			if err != nil || result.IsError || strings.TrimSpace(result.Flatten()) != "command-ok" {
				t.Fatalf("custom command failed: %v %s", err, result.Flatten())
			}
			if len(tc.Env) != len(env) {
				t.Fatal("caller ToolContext mutated")
			}
		}
	}
	for _, key := range serverSecretCanaryKeys {
		if os.Getenv(key) != "server-env-canary" {
			t.Fatalf("parent environment changed: %s", key)
		}
	}
}

func TestStdioMCPEnvironment(t *testing.T) {
	setServerSecretCanaries(t)
	interp := pythonFixture(t)
	fixture, err := filepath.Abs("testdata/processenv_mcp.py")
	if err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		expected := ""
		env := map[string]string{}
		if explicit {
			expected = "explicit-canary"
			key := "OPENAI_API_KEY"
			if runtime.GOOS == "windows" {
				key = "openai_api_key"
			}
			env[key] = expected
		}
		args, _ := json.Marshal([]string{fixture, expected})
		rawEnv, _ := json.Marshal(env)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		client, err := connectMCP(ctx, &db.MCPServer{Name: "env-local", Transport: "stdio", Command: interp, Args: args, Env: rawEnv})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		tools, err := client.Tools(ctx)
		client.Close()
		cancel()
		if err != nil || len(tools) != 1 || tools[0].Name() != "mcp__env-local__environment_ok" {
			t.Fatalf("stdio environment/handshake failed: %v", err)
		}
	}
	for _, key := range serverSecretCanaryKeys {
		if os.Getenv(key) != "server-env-canary" {
			t.Fatalf("parent environment changed: %s", key)
		}
	}
}

func TestRemoteMCPHeaders(t *testing.T) {
	for _, transport := range []string{"http", "sse"} {
		t.Run(transport, func(t *testing.T) {
			messages := make(chan []byte, 4)
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer env-explicit" || r.Header.Get("OPENAI_API_KEY") != "header-canary" || r.Header.Get("ANTHROPIC_API_KEY") != "" {
					t.Error("explicit remote headers changed")
					http.Error(w, "header check failed", 400)
					return
				}
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "event: endpoint\ndata: /messages\n\n")
					w.(http.Flusher).Flush()
					for {
						select {
						case payload := <-messages:
							fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
							w.(http.Flusher).Flush()
						case <-r.Context().Done():
							return
						}
					}
				}
				var request struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					http.Error(w, "invalid request", 400)
					return
				}
				if request.ID == 0 {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				var result any
				switch request.Method {
				case "initialize":
					result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}}
				case "tools/list":
					result = map[string]any{"tools": []any{}}
				default:
					t.Errorf("unexpected protocol method: %s", request.Method)
				}
				payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
				if err != nil {
					t.Error(err)
					return
				}
				if transport == "sse" {
					messages <- payload
					w.WriteHeader(http.StatusAccepted)
				} else {
					w.Header().Set("Content-Type", "application/json")
					w.Write(payload)
				}
			}))
			defer fixture.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			rawEnv, _ := json.Marshal(map[string]string{"Authorization": "Bearer env-explicit", "OPENAI_API_KEY": "header-canary"})
			client, err := connectMCP(ctx, &db.MCPServer{Name: "env-remote", Transport: transport, URL: fixture.URL, Env: rawEnv})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if _, err := client.Tools(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
