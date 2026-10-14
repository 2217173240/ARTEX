//go:build !windows

package server

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Python descendants inherit a dedicated process group. Cancel is invoked by
// os/exec only for a started command, before its Wait completes.
func configurePythonProcess(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.WaitDelay = time.Second
	c.Cancel = func() error {
		if c.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
}

func pythonCombinedOutput(_ context.Context, c *exec.Cmd) ([]byte, error) { return c.CombinedOutput() }
