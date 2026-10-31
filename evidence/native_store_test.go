package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func nativeBodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func assertNativeStagingEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, ".staging"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging retains %d files", len(entries))
	}
}

func TestEvidenceNativeWriteBody(t *testing.T) {
	for _, body := range [][]byte{{0, 1, 255, '\r', '\n'}, {}} {
		t.Run(nativeBodyHash(body)[:8], func(t *testing.T) {
			s := &Store{Dir: t.TempDir()}
			want := nativeBodyHash(body)
			hash, err := s.writeBody(bytes.NewReader(body), int64(len(body)), want)
			if err != nil || hash != want {
				t.Fatalf("writeBody = %q, %v; want %q", hash, err, want)
			}
			path, err := hashPath(s.Dir, hash)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("stored body = %x, %v; want %x", got, err, body)
			}
			if hash, err = s.writeBody(bytes.NewReader(body), int64(len(body)), ""); err != nil || hash != want {
				t.Fatalf("repeat write = %q, %v", hash, err)
			}
			after, err := os.Stat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("repeat replaced existing blob: %v", err)
			}
			assertNativeStagingEmpty(t, s.Dir)
		})
	}
}

func TestEvidenceNativeRejectInvalidBody(t *testing.T) {
	body := []byte("evidence")
	for _, tc := range []struct {
		name   string
		length int64
		hash   string
	}{
		{"short", int64(len(body) + 1), ""},
		{"long", int64(len(body) - 1), ""},
		{"hash", int64(len(body)), nativeBodyHash([]byte("other"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Store{Dir: t.TempDir()}
			if _, err := s.writeBody(bytes.NewReader(body), tc.length, tc.hash); err == nil {
				t.Fatal("invalid body accepted")
			}
			path, err := hashPath(s.Dir, nativeBodyHash(body))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("invalid body published: %v", err)
			}
			assertNativeStagingEmpty(t, s.Dir)
		})
	}
	t.Run("closed reader", func(t *testing.T) {
		s := &Store{Dir: t.TempDir()}
		f, err := os.CreateTemp(t.TempDir(), "reader-")
		if err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err = s.writeBody(f, 0, ""); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("closed reader error = %v", err)
		}
		assertNativeStagingEmpty(t, s.Dir)
	})
}

func TestEvidenceNativeRejectCorruptBlob(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	body := []byte("valid")
	hash, err := s.writeBody(bytes.NewReader(body), int64(len(body)), "")
	if err != nil {
		t.Fatal(err)
	}
	path, err := hashPath(s.Dir, hash)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("wrong")
	if err = os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.writeBody(bytes.NewReader(body), int64(len(body)), hash); err == nil {
		t.Fatal("corrupt existing blob accepted")
	}
	f, _, err := s.OpenBody(db.TrafficEvidenceSnapshot{ReqHash: hash, ReqLen: int64(len(body))}, "request")
	if f != nil {
		f.Close()
	}
	if err == nil {
		t.Fatal("OpenBody accepted corrupt blob")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, corrupt) {
		t.Fatalf("corrupt blob overwritten: %x, %v", got, err)
	}
	assertNativeStagingEmpty(t, s.Dir)
}

func TestEvidenceNativeStagingObstruction(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(s.Dir, ".staging"), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writeBody(bytes.NewReader(nil), 0, ""); err == nil {
		t.Fatal("staging file accepted as directory")
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "blobs")); !os.IsNotExist(err) {
		t.Fatalf("body published despite staging obstruction: %v", err)
	}
}

func TestEvidenceNativeDirectorySync(t *testing.T) {
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		d, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("read-only directory Sync capability: %v", d.Sync())
		if err = d.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := syncDir(dir); err != nil {
		t.Fatalf("existing directory: %v", err)
	}
	if err := syncDir(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing directory error = %v", err)
	}
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syncDir(path); err == nil {
		t.Fatal("regular file accepted as directory")
	}
}
