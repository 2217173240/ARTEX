package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

func TestExecutableToolEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell assertions; Windows merge policy is covered in processenv")
	}
	keys := []string{"ARTEX_PG_DSN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "ARTEX_LLM_BASE_URL", "ARTEX_LLM_PROXY"}
	for _, key := range keys {
		t.Setenv(key, "server-env-canary")
	}
	t.Setenv("ARTEX_LLM_CALL_TIMEOUT", "37")
	t.Setenv("AGENT_CORE_DISABLE_BACKGROUND_TASKS", "")
	t.Setenv("AGENT_CORE_DISABLE_INTERACTIVE_SHELL", "")
	t.Setenv("TOOL_ENV_PRESERVED", "runtime-canary")
	work := t.TempDir()
	manager, err := actool.NewManager(fmt.Sprintf("env-policy-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	directChecks := []string{`test "$ARTEX_LLM_CALL_TIMEOUT" = 37`}
	for _, key := range keys {
		directChecks = append(directChecks, `test -z "$`+key+`"`)
	}
	directInput, _ := json.Marshal(map[string]any{"command": strings.Join(directChecks, " && ") + ` && printf direct-ok`})
	directResult, err := actool.NewBash().Call(ctx, directInput, &actool.ToolContext{WorkingDir: work, Env: proxyEnv("", "")})
	if err != nil || directResult.IsError || strings.TrimSpace(directResult.Flatten()) != "direct-ok" {
		t.Fatalf("direct environment failed: %v %s", err, directResult.Flatten())
	}
	for _, explicit := range []bool{false, true} {
		env := proxyEnv("http://proxy.invalid:8111", "canary.pem")
		if explicit {
			env = append(env, "OPENAI_API_KEY=explicit-canary")
		}
		checks := []string{`test -n "$PATH"`, `test -n "$HOME"`, `test "$ARTEX_LLM_CALL_TIMEOUT" = 37`, `test "$TOOL_ENV_PRESERVED" = runtime-canary`, `test "$HTTP_PROXY" = http://proxy.invalid:8111`}
		for _, key := range keys {
			if key == "OPENAI_API_KEY" && explicit {
				checks = append(checks, `test "$OPENAI_API_KEY" = explicit-canary`)
			} else {
				checks = append(checks, `test -z "$`+key+`"`)
			}
		}
		for _, key := range []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO", "NODE_EXTRA_CA_CERTS"} {
			checks = append(checks, `test "$`+key+`" = canary.pem`)
		}
		command := strings.Join(checks, " && ") + ` && printf 'environment-ok'`
		for _, mode := range []string{"sync", "managed", "background", "monitor", "pty"} {
			t.Run(fmt.Sprintf("%s/explicit=%t", mode, explicit), func(t *testing.T) {
				tc := &actool.ToolContext{WorkingDir: work, Env: env}
				tool := actool.NewBash()
				before := map[string]bool{}
				for _, task := range manager.List() {
					before[task.ID] = true
				}
				input := map[string]any{"command": command, "timeout_ms": 10000}
				if mode != "sync" {
					tc.Tasks = manager
				}
				switch mode {
				case "background":
					input["run_in_background"] = true
				case "monitor":
					tool = actool.NewMonitor()
				case "pty":
					tool = actool.NewShellOpen()
					input["mode"], input["quiet_ms"], input["startup_grace_ms"] = "interactive", 50, 1
				}
				raw, _ := json.Marshal(input)
				result, err := tool.Call(ctx, raw, tc)
				if err != nil || result.IsError {
					t.Fatalf("tool failed: %v %s", err, result.Flatten())
				}
				out := result.Flatten()
				if mode == "background" || mode == "monitor" {
					for _, task := range manager.List() {
						if before[task.ID] {
							continue
						}
						info, done := manager.Wait(ctx, task.ID, 10*time.Second)
						if !done || info.ExitCode != 0 {
							t.Fatalf("controlled task did not succeed: %+v", info)
						}
						if output, err := manager.Output(task.ID, 4096); err == nil && strings.Contains(output, "environment-ok") {
							out = output
						}
					}
				}
				if !strings.Contains(out, "environment-ok") {
					t.Fatalf("missing successful environment checks: %s", out)
				}
			})
		}
	}
	for _, key := range keys {
		if os.Getenv(key) != "server-env-canary" {
			t.Fatalf("parent environment changed: %s", key)
		}
	}
	if callTimeoutFromEnv() != 37*time.Second {
		t.Fatal("dynamic provider timeout changed")
	}
}
