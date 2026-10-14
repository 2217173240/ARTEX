package server

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

func configurePythonProcess(c *exec.Cmd) { c.WaitDelay = time.Second }

// Block user code until the interpreter is assigned to a handle-owned job.
// A PID-based taskkill can target a reused PID after the interpreter exits.
const pythonJobBootstrap = `import sys,runpy,os
sys.stdin.buffer.read(1)
p=sys.argv[1]
sys.argv=[p]
sys.path[0]=os.path.dirname(p)
runpy.run_path(p,run_name="__main__")`

type pythonGateReader struct {
	ready <-chan struct{}
	input io.Reader
}

func (r pythonGateReader) Read(p []byte) (int, error) { <-r.ready; return r.input.Read(p) }

func pythonCombinedOutput(ctx context.Context, c *exec.Cmd) ([]byte, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(job)
	c.Cancel = func() error {
		if c.Process == nil {
			return os.ErrProcessDone
		}
		return windows.TerminateJobObject(job, 1)
	}
	script, err := filepath.Abs(c.Args[1])
	if err != nil {
		return nil, err
	}
	c.Args = []string{c.Args[0], "-c", pythonJobBootstrap, script}
	ready := make(chan struct{})
	c.Stdin = pythonGateReader{ready, io.MultiReader(bytes.NewReader([]byte{1}), c.Stdin)}
	var output bytes.Buffer
	c.Stdout = &output
	c.Stderr = &output
	if err = c.Start(); err != nil {
		close(ready)
		return nil, err
	}
	var assignErr error
	err = c.Process.WithHandle(func(handle uintptr) { assignErr = windows.AssignProcessToJobObject(job, windows.Handle(handle)) })
	if err == nil {
		err = assignErr
	}
	if err != nil {
		_ = c.Process.Kill()
		close(ready)
		_ = c.Wait()
		return output.Bytes(), err
	}
	close(ready)
	// Cancellation may have terminated the empty job while Start/Assign ran.
	// The bootstrap still cannot have launched descendants before assignment.
	// Re-terminate the now populated job for the canceled-before-assignment
	// case. Job handles are closed without KILL_ON_JOB_CLOSE on normal completion.
	if ctx.Err() != nil {
		_ = windows.TerminateJobObject(job, 1)
	}
	err = c.Wait()
	return output.Bytes(), err
}
