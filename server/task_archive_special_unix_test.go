//go:build unix

package server

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
	"golang.org/x/sys/unix"
)

func TestTaskArchivePackageSkipsSpecialFiles(t *testing.T) {
	// Keep the socket path short enough for the macOS Unix socket path limit.
	dir, err := os.MkdirTemp("/tmp", "artex-archive-special-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	payload := filepath.Join(dir, "payload")
	if err := os.Mkdir(payload, archiveDirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, "keep.txt"), []byte("regular evidence"), archiveFileMode); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(payload, "pipe")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(payload, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	archivePath := filepath.Join(dir, "archive.tar.zst")
	type result struct {
		checksum string
		err      error
	}
	done := make(chan result, 1)
	go func() {
		_, _, checksum, err := writeTaskArchivePackage(archivePath, payload, &pgdb.TaskArchiveSnapshot{
			FormatVersion: pgdb.TaskArchiveFormatVersion, TaskID: 42,
		})
		done <- result{checksum, err}
	}()
	var got result
	select {
	case got = <-done:
	case <-time.After(time.Second):
		// Unblock the old implementation's FIFO open before failing, so the
		// regression does not leave a writer running against its test directory.
		fd, err := unix.Open(fifo, unix.O_WRONLY|unix.O_NONBLOCK, 0)
		if err == nil {
			_ = unix.Close(fd)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("archive blocked on a special file")
	}
	if got.err != nil {
		t.Fatalf("archive should skip unsupported file types: %v", got.err)
	}
	extracted := filepath.Join(dir, "restore")
	if err := extractTaskArchivePackage(archivePath, got.checksum, extracted); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(extracted, "keep.txt")); err != nil || string(body) != "regular evidence" {
		t.Fatalf("regular file was not preserved: %q, %v", body, err)
	}
	for _, name := range []string{"pipe", "socket"} {
		if _, err := os.Lstat(filepath.Join(extracted, name)); !os.IsNotExist(err) {
			t.Fatalf("special file %s should be omitted, got %v", name, err)
		}
	}
}
