package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pythonFixture(t *testing.T) string {
	t.Helper()
	if configured := os.Getenv("ARTEX_TEST_PYTHON"); configured != "" {
		p, err := exec.LookPath(configured)
		if err != nil {
			t.Fatalf("configured Python interpreter %q unavailable: %v", configured, err)
		}
		return p
	}
	p, err := exec.LookPath("python3")
	if err != nil {
		p, err = exec.LookPath("python")
	}
	if err != nil {
		t.Skip("Python 3 is required for lifecycle fixture")
	}
	return p
}

func TestPythonLifecycleNormalAndStartFailure(t *testing.T) {
	p := pythonFixture(t)
	body, err := execPython(context.Background(), p, "normal", `import json,os,sys
p=json.load(sys.stdin)
print(p['value'],os.environ['TOOL_VALUE'],os.environ['FIXTURE_ENV'],os.getcwd())
print(len(sys.argv)==1,sys.argv[0]==__file__,os.path.realpath(sys.path[0])==os.path.realpath(os.path.dirname(__file__)))
print(p['unicode'])`, map[string]any{"value": "fixture", "unicode": "参数-é"}, t.TempDir(), []string{"FIXTURE_ENV=isolated", "PYTHONIOENCODING=utf-8"}, time.Second*5)
	if err != nil || !strings.Contains(body, "fixture fixture isolated") || !strings.Contains(body, "True True True") || !strings.Contains(body, "参数-é") || strings.Contains(body, "[exit:") {
		t.Fatalf("normal output %q, err %v", body, err)
	}
	body, err = execPython(context.Background(), filepath.Join(t.TempDir(), "missing-python"), "missing", "", nil, t.TempDir(), nil, time.Second)
	if err != nil || !strings.Contains(body, "[exit:") {
		t.Fatalf("start failure contract %q, %v", body, err)
	}
}

func TestPythonLifecycleTree(t *testing.T) {
	p := pythonFixture(t)
	for _, timeout := range []bool{false, true} {
		name := "cancel"
		if timeout {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			heartbeat := filepath.Join(dir, "heartbeat")
			child := `import time,pathlib; p=pathlib.Path('heartbeat'); [(p.write_text(str(i)),time.sleep(.03)) for i in range(200)]`
			code := `import subprocess,time
subprocess.Popen([` + pythonStringLiteral(p) + `, '-c', ` + pythonStringLiteral(child) + `])
print('started',flush=True)
time.sleep(10)`
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			limit := time.Second * 10
			if timeout {
				limit = time.Millisecond * 1500
			}
			done := make(chan string, 1)
			go func() { body, _ := execPython(ctx, p, "tree", code, nil, dir, nil, limit); done <- body }()
			deadline := time.Now().Add(time.Second * 5)
			for {
				if _, err := os.Stat(heartbeat); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child did not start")
				}
				time.Sleep(time.Millisecond * 10)
			}
			start := time.Now()
			if !timeout {
				cancel()
			}
			select {
			case body := <-done:
				if !strings.Contains(body, "started") {
					t.Fatalf("lost output: %q", body)
				}
				if timeout && !strings.Contains(body, "[超时终止]") {
					t.Fatalf("missing timeout note: %q", body)
				}
			case <-time.After(time.Second * 3):
				t.Fatal("cancellation blocked on descendant pipes")
			}
			if time.Since(start) > time.Second*3 {
				t.Fatal("unbounded cancellation")
			}
			before, _ := os.ReadFile(heartbeat)
			time.Sleep(time.Millisecond * 150)
			after, _ := os.ReadFile(heartbeat)
			if string(before) != string(after) {
				t.Fatalf("descendant survived: %q -> %q", before, after)
			}
		})
	}
}

func TestPythonLifecycleAlreadyCanceled(t *testing.T) {
	p := pythonFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	body, err := execPython(ctx, p, "canceled", `open('should-not-exist','w').write('started')`, nil, dir, nil, time.Second)
	if err != nil || !strings.Contains(body, "[exit:") {
		t.Fatalf("cancel contract: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatal("pre-canceled script executed")
	}
}

func TestPythonLifecycleNormalInheritedPipeBound(t *testing.T) {
	p := pythonFixture(t)
	// Descendants of a normally completed script retain the existing background
	// operation semantics. They must not hold the output collector indefinitely.
	code := `import subprocess
subprocess.Popen([` + pythonStringLiteral(p) + `,'-c','import time; time.sleep(2)'])
print('parent finished',flush=True)`
	start := time.Now()
	body, err := execPython(context.Background(), p, "pipe", code, nil, t.TempDir(), nil, time.Second*5)
	elapsed := time.Since(start)
	if err != nil || !strings.Contains(body, "parent finished") {
		t.Fatalf("normal inherited pipe: %q, %v", body, err)
	}
	if elapsed > time.Millisecond*1800 {
		t.Fatalf("output waited for background child: %s", elapsed)
	}
	// The bounded synthetic child exits naturally; wait before fixture teardown.
	if remaining := time.Millisecond*2200 - elapsed; remaining > 0 {
		time.Sleep(remaining)
	}
}

func pythonStringLiteral(s string) string { b, _ := json.Marshal(s); return string(b) }
