package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorkspaceInternalStorageGuard(t *testing.T) {
	s, dir := workspaceServer(t)
	for _, name := range []string{"traffic/_ca", "traffic/_index", "traffic/host", "traffic/_caX"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"traffic/_ca/key", "traffic/_index/index"} {
		workspacePut(t, filepath.Join(dir, name), "synthetic internal data")
	}
	workspaceSymlink(t, "traffic", filepath.Join(dir, "capture"))
	workspaceSymlink(t, "traffic/_ca", filepath.Join(dir, "ca-alias"))
	workspaceSymlink(t, "traffic/_ca/key", filepath.Join(dir, "key-alias"))
	workspaceSymlink(t, "../_index", filepath.Join(dir, "traffic/host/index-alias"))
	for _, path := range []string{"traffic/_ca/key", "traffic/_index/index", "TRAFFIC/_CA/key", "capture/_ca/key", "ca-alias/key", "key-alias", "traffic/host/index-alias/index"} {
		for _, op := range []struct {
			name, method string
			handler      http.HandlerFunc
		}{
			{"read", http.MethodGet, s.wsRead}, {"download", http.MethodGet, s.wsDownload},
			{"write", http.MethodPost, s.wsWrite}, {"mkdir", http.MethodPost, s.wsMkdir},
			{"delete", http.MethodDelete, s.wsDelete},
		} {
			t.Run(op.name+"/"+path, func(t *testing.T) {
				if w := workspaceRequest(t, op.handler, op.method, path, "replacement"); w.Code < 400 {
					t.Fatalf("allowed internal path: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
	for _, path := range []string{"traffic", "capture", "traffic/_ca", "traffic/_index"} {
		if w := workspaceRequest(t, s.wsDelete, http.MethodDelete, path, ""); w.Code < 400 {
			t.Fatalf("deleted internal ancestor %q", path)
		}
	}
	for _, path := range []string{"ca-alias", "capture/_index"} {
		if w := workspaceRequest(t, s.wsList, http.MethodGet, path, ""); w.Code < 400 {
			t.Fatalf("listed internal alias %q", path)
		}
		w := httptest.NewRecorder()
		s.wsUpload(w, workspaceUploadRequest(t, path, "new", strings.NewReader("replacement")))
		if w.Code < 400 {
			t.Fatalf("uploaded to internal alias %q", path)
		}
	}
	for _, path := range []string{"", "traffic", "capture", "traffic/host"} {
		w := workspaceRequest(t, s.wsList, http.MethodGet, path, "")
		var result struct {
			Entries []wsEntry `json:"entries"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
			t.Fatalf("list %q: %d %s", path, w.Code, w.Body.String())
		}
		for _, entry := range result.Entries {
			if entry.Name == "_ca" || entry.Name == "_index" || entry.Name == "ca-alias" || entry.Name == "key-alias" || entry.Name == "index-alias" {
				t.Fatalf("internal listing entry: %+v", entry)
			}
		}
	}
	// Validate a complete multipart request before creating the first file.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, name := range []string{"ordinary", "key-alias"} {
		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("replacement")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/workspace/upload", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	s.wsUpload(w, r)
	if w.Code < 400 {
		t.Fatalf("uploaded reserved final name: %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "ordinary")); !os.IsNotExist(err) {
		t.Fatalf("multipart wrote partial output: %v", err)
	}
	for _, name := range []string{"traffic/_ca/key", "traffic/_index/index"} {
		workspaceAssertFile(t, filepath.Join(dir, name), "synthetic internal data")
	}
	for _, path := range []string{"traffic/host/user.txt", "traffic/_caX/user.txt"} {
		if w := workspaceRequest(t, s.wsWrite, http.MethodPost, path, "user data"); w.Code != 200 {
			t.Fatalf("ordinary write: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestWorkspaceDeleteAliasPreservesTarget(t *testing.T) {
	s, dir := workspaceServer(t)
	if err := os.Mkdir(filepath.Join(dir, "ordinary"), 0o755); err != nil {
		t.Fatal(err)
	}
	workspacePut(t, filepath.Join(dir, "ordinary/file"), "user data")
	workspaceSymlink(t, "ordinary", filepath.Join(dir, "alias"))
	if w := workspaceRequest(t, s.wsDelete, http.MethodDelete, "alias", ""); w.Code != 200 {
		t.Fatalf("unlink alias: %d %s", w.Code, w.Body.String())
	}
	workspaceAssertFile(t, filepath.Join(dir, "ordinary/file"), "user data")
	if _, err := os.Lstat(filepath.Join(dir, "alias")); !os.IsNotExist(err) {
		t.Fatalf("alias remains: %v", err)
	}
}

func TestWorkspacePlatformInternalAliases(t *testing.T) {
	s, dir := workspaceServer(t)
	if err := os.MkdirAll(filepath.Join(dir, "traffic/_ca"), 0o755); err != nil {
		t.Fatal(err)
	}
	workspacePut(t, filepath.Join(dir, "traffic/_ca/key"), "synthetic internal data")
	paths := []string{"TRAFFIC/_CA/key"}
	if runtime.GOOS == "windows" {
		paths = append(paths, "traffic./_ca./key", "traffic /_ca /key")
	}
	for _, path := range paths {
		// The native filesystem determines which alternate spellings exist.
		if _, err := os.Stat(filepath.Join(dir, path)); err != nil {
			continue
		}
		if w := workspaceRequest(t, s.wsRead, http.MethodGet, path, ""); w.Code < 400 {
			t.Fatalf("native equivalent spelling exposed internal data: %q", path)
		}
	}
}

func TestWorkspaceUploadRechecksInternalAliasAfterParsing(t *testing.T) {
	s, dir := workspaceServer(t)
	for _, name := range []string{"uploads", "traffic/_ca"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	workspacePut(t, filepath.Join(dir, "traffic/_ca/key"), "synthetic internal data")
	workspaceSymlink(t, "traffic/_ca", filepath.Join(dir, "probe"))
	r := workspaceUploadRequest(t, "uploads", "key", strings.NewReader("replacement"))
	r.Body = &workspaceReadHook{Reader: r.Body, hook: func() {
		if err := os.Rename(filepath.Join(dir, "uploads"), filepath.Join(dir, "held")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("traffic/_ca", filepath.Join(dir, "uploads")); err != nil {
			t.Fatal(err)
		}
	}}
	w := httptest.NewRecorder()
	s.wsUpload(w, r)
	if w.Code < 400 {
		t.Fatalf("upload exposed replacement internal alias: %d", w.Code)
	}
	workspaceAssertFile(t, filepath.Join(dir, "traffic/_ca/key"), "synthetic internal data")
}

func TestWorkspaceTrafficAliasProtectsBackingParents(t *testing.T) {
	s, dir := workspaceServer(t)
	if err := os.MkdirAll(filepath.Join(dir, "backing/capture/_ca"), 0o755); err != nil {
		t.Fatal(err)
	}
	workspacePut(t, filepath.Join(dir, "backing/capture/_ca/key"), "synthetic internal data")
	workspaceSymlink(t, "backing/capture", filepath.Join(dir, "traffic"))
	for _, path := range []string{"backing", "backing/capture", "traffic"} {
		if w := workspaceRequest(t, s.wsDelete, http.MethodDelete, path, ""); w.Code < 400 {
			t.Fatalf("deleted internal backing parent %q", path)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "BACKING")); err == nil {
		if w := workspaceRequest(t, s.wsDelete, http.MethodDelete, "BACKING", ""); w.Code < 400 {
			t.Fatal("deleted native case alias of backing parent")
		}
	}
	workspaceAssertFile(t, filepath.Join(dir, "backing/capture/_ca/key"), "synthetic internal data")
}

func TestWorkspaceUnavailableInternalStorageKeepsOrdinaryFilesUsable(t *testing.T) {
	s, dir := workspaceServer(t)
	outside := t.TempDir()
	workspaceSymlink(t, outside, filepath.Join(dir, "traffic"))
	workspacePut(t, filepath.Join(dir, "ordinary"), "user data")
	if w := workspaceRequest(t, s.wsDelete, http.MethodDelete, "ordinary", ""); w.Code != 200 {
		t.Fatalf("ordinary delete: %d %s", w.Code, w.Body.String())
	}
	for _, handler := range []http.HandlerFunc{s.wsRead, s.wsDownload, s.wsDelete} {
		if w := workspaceRequest(t, handler, http.MethodGet, "missing", ""); w.Code != 404 {
			t.Fatalf("missing file: %d %s", w.Code, w.Body.String())
		}
	}
}
