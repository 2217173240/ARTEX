package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func workspaceServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	return &Server{m: &Manager{dir: dir}}, dir
}

func workspaceRequest(t *testing.T, handler http.HandlerFunc, method, path, content string) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if method == http.MethodPost {
		data, err := json.Marshal(map[string]string{"path": path, "content": content})
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	r := httptest.NewRequest(method, "/api/workspace?path="+url.QueryEscape(path), body)
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func workspaceUploadRequest(t *testing.T, path, filename string, data io.Reader) *http.Request {
	t.Helper()
	var framing bytes.Buffer
	writer := multipart.NewWriter(&framing)
	if _, err := writer.CreateFormFile("file", filename); err != nil {
		t.Fatal(err)
	}
	prefix := append([]byte(nil), framing.Bytes()...)
	framing.Reset()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/workspace/upload?path="+url.QueryEscape(path),
		io.MultiReader(bytes.NewReader(prefix), data, bytes.NewReader(framing.Bytes())))
	r.Header.Set("Content-Type", writer.FormDataContentType())
	return r
}

func workspaceSymlink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, errors.ErrUnsupported) || errors.Is(err, syscall.ENOSYS) ||
			(runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314))) { // ERROR_PRIVILEGE_NOT_HELD
			t.Skipf("symlink creation unsupported or not permitted: %v", err)
		}
		t.Fatal(err)
	}
}

func workspacePut(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func workspaceAssertFile(t *testing.T, path, content string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatalf("file %q = %q, %v; want %q", path, data, err, content)
	}
}

func TestWorkspaceRejectsEscapingSymlinks(t *testing.T) {
	s, dir := workspaceServer(t)
	outside := t.TempDir()
	canary := filepath.Join(outside, "canary.txt")
	workspacePut(t, canary, "external canary")
	workspaceSymlink(t, canary, filepath.Join(dir, "file-link"))
	workspaceSymlink(t, outside, filepath.Join(dir, "dir-link"))
	// A relative link which climbs above the workspace must also be rejected.
	relOutside, err := filepath.Rel(dir, outside)
	if err != nil {
		t.Fatal(err)
	}
	workspaceSymlink(t, relOutside, filepath.Join(dir, "relative-dir-link"))
	for _, path := range []string{"file-link", "dir-link/canary.txt", "relative-dir-link/canary.txt"} {
		for _, op := range []struct {
			name, method string
			handler      http.HandlerFunc
		}{
			{"read", http.MethodGet, s.wsRead},
			{"download", http.MethodGet, s.wsDownload},
			{"write", http.MethodPost, s.wsWrite},
			{"delete", http.MethodDelete, s.wsDelete},
		} {
			t.Run(op.name+"/"+path, func(t *testing.T) {
				w := workspaceRequest(t, op.handler, op.method, path, "overwritten")
				if w.Code < 400 {
					t.Fatalf("escaping path returned %d: %s", w.Code, w.Body.String())
				}
				workspaceAssertFile(t, canary, "external canary")
			})
		}
	}
	for _, path := range []string{"dir-link", "relative-dir-link"} {
		t.Run("list/"+path, func(t *testing.T) {
			if w := workspaceRequest(t, s.wsList, http.MethodGet, path, ""); w.Code < 400 {
				t.Fatalf("escaping list returned %d: %s", w.Code, w.Body.String())
			}
		})
		t.Run("mkdir/"+path, func(t *testing.T) {
			if w := workspaceRequest(t, s.wsMkdir, http.MethodPost, path+"/created", ""); w.Code < 400 {
				t.Fatalf("escaping mkdir returned %d: %s", w.Code, w.Body.String())
			}
			if _, err := os.Stat(filepath.Join(outside, "created")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("external directory creation: %v", err)
			}
		})
		t.Run("delete-directory/"+path, func(t *testing.T) {
			if w := workspaceRequest(t, s.wsDelete, http.MethodDelete, path, ""); w.Code < 400 {
				t.Fatalf("escaping directory delete returned %d", w.Code)
			}
			workspaceAssertFile(t, canary, "external canary")
		})
		t.Run("upload-directory/"+path, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.wsUpload(w, workspaceUploadRequest(t, path, "canary.txt", strings.NewReader("overwritten")))
			if w.Code < 400 {
				t.Fatalf("escaping upload returned %d: %s", w.Code, w.Body.String())
			}
			workspaceAssertFile(t, canary, "external canary")
		})
	}
	// The directory is valid but the uploaded final filename resolves outside.
	w := httptest.NewRecorder()
	s.wsUpload(w, workspaceUploadRequest(t, "", "file-link", strings.NewReader("overwritten")))
	if w.Code < 400 {
		t.Fatalf("escaping upload destination returned %d: %s", w.Code, w.Body.String())
	}
	workspaceAssertFile(t, canary, "external canary")
}

func TestWorkspaceNormalFlowsAndRelativeSymlinks(t *testing.T) {
	s, dir := workspaceServer(t)
	if w := workspaceRequest(t, s.wsMkdir, http.MethodPost, "nested", ""); w.Code != 200 {
		t.Fatalf("mkdir: %d %s", w.Code, w.Body.String())
	}
	workspaceSymlink(t, "nested", filepath.Join(dir, "alias"))
	if w := workspaceRequest(t, s.wsWrite, http.MethodPost, "/alias/deeper/../文件.txt", "abcdef"); w.Code != 200 {
		t.Fatalf("write: %d %s", w.Code, w.Body.String())
	}
	workspaceSymlink(t, filepath.Join("nested", "文件.txt"), filepath.Join(dir, "file-alias"))
	w := workspaceRequest(t, s.wsRead, http.MethodGet, "file-alias", "")
	var read struct {
		Content string `json:"content"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &read); err != nil || w.Code != 200 || read.Content != "abcdef" || read.Path != "file-alias" {
		t.Fatalf("read: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/workspace/download?path="+url.QueryEscape("alias/文件.txt"), nil)
	r.Header.Set("Range", "bytes=1-3")
	w = httptest.NewRecorder()
	s.wsDownload(w, r)
	if w.Code != http.StatusPartialContent || w.Body.String() != "bcd" || w.Header().Get("Content-Range") != "bytes 1-3/6" {
		t.Fatalf("range download: %d %s %v", w.Code, w.Body.String(), w.Header())
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "filename*=UTF-8''"+url.PathEscape("文件.txt")) {
		t.Fatalf("download filename: %s", w.Header().Get("Content-Disposition"))
	}
	w = httptest.NewRecorder()
	s.wsUpload(w, workspaceUploadRequest(t, "alias", "../uploaded.txt", strings.NewReader("uploaded")))
	if w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	workspaceAssertFile(t, filepath.Join(dir, "nested", "uploaded.txt"), "uploaded")
	w = workspaceRequest(t, s.wsList, http.MethodGet, "alias", "")
	var list struct {
		Path    string    `json:"path"`
		Entries []wsEntry `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || list.Path != "alias" || len(list.Entries) != 2 {
		t.Fatalf("list: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	for _, entry := range list.Entries {
		if !strings.HasPrefix(entry.Path, "alias/") {
			t.Fatalf("list entry path: %+v", entry)
		}
	}
	if w := workspaceRequest(t, s.wsDelete, http.MethodDelete, "alias/uploaded.txt", ""); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "nested", "uploaded.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted file remains: %v", err)
	}
	for _, path := range []string{"", "/", ".", "a/.."} {
		if w := workspaceRequest(t, s.wsDelete, http.MethodDelete, path, ""); w.Code != 400 {
			t.Fatalf("root deletion %q: %d %s", path, w.Code, w.Body.String())
		}
		if w := workspaceRequest(t, s.wsWrite, http.MethodPost, path, "replacement"); w.Code != 400 {
			t.Fatalf("root write %q: %d %s", path, w.Code, w.Body.String())
		}
		if w := workspaceRequest(t, s.wsMkdir, http.MethodPost, path, ""); w.Code != 400 {
			t.Fatalf("root mkdir %q: %d %s", path, w.Code, w.Body.String())
		}
	}
	workspaceAssertFile(t, filepath.Join(dir, "nested", "文件.txt"), "abcdef")
}

type workspaceReadHook struct {
	io.Reader
	hook   func()
	closed bool
}

func (r *workspaceReadHook) Read(p []byte) (int, error) {
	if r.hook != nil {
		hook := r.hook
		r.hook = nil
		hook()
	}
	return r.Reader.Read(p)
}

func (r *workspaceReadHook) Close() error {
	r.closed = true
	return nil
}

func TestWorkspaceUploadKeepsOpenedRootAcrossReplacement(t *testing.T) {
	if runtime.GOOS == "js" || runtime.GOOS == "plan9" {
		t.Skip("os.Root does not hold directory handles across rename on this platform")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "workspace")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	workspacePut(t, filepath.Join(outside, "canary.txt"), "external canary")
	// Check symlink support before the body hook, where skipping would bypass cleanup.
	probe := filepath.Join(parent, "symlink-probe")
	workspaceSymlink(t, outside, probe)
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{dir: dir}}
	r := workspaceUploadRequest(t, "", "canary.txt", strings.NewReader("uploaded inside"))
	held := filepath.Join(parent, "held-workspace")
	body := &workspaceReadHook{Reader: r.Body, hook: func() {
		if err := os.Rename(dir, held); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, dir); err != nil {
			t.Fatal(err)
		}
	}}
	r.Body = body
	w := httptest.NewRecorder()
	s.wsUpload(w, r)
	if w.Code != 200 || !body.closed {
		t.Fatalf("upload: %d %s; body closed=%v", w.Code, w.Body.String(), body.closed)
	}
	workspaceAssertFile(t, filepath.Join(outside, "canary.txt"), "external canary")
	workspaceAssertFile(t, filepath.Join(held, "canary.txt"), "uploaded inside")
}

func TestWorkspaceUploadRejectsDirectoryReplacementAfterCheck(t *testing.T) {
	s, dir := workspaceServer(t)
	uploadDir := filepath.Join(dir, "uploads")
	if err := os.Mkdir(uploadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	workspacePut(t, filepath.Join(outside, "canary.txt"), "external canary")
	workspaceSymlink(t, outside, filepath.Join(dir, "probe"))
	r := workspaceUploadRequest(t, "uploads", "canary.txt", strings.NewReader("overwritten"))
	r.Body = &workspaceReadHook{Reader: r.Body, hook: func() {
		if err := os.Rename(uploadDir, filepath.Join(dir, "held-uploads")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, uploadDir); err != nil {
			t.Fatal(err)
		}
	}}
	w := httptest.NewRecorder()
	s.wsUpload(w, r)
	if w.Code < 400 {
		t.Fatalf("replaced upload directory returned %d: %s", w.Code, w.Body.String())
	}
	workspaceAssertFile(t, filepath.Join(outside, "canary.txt"), "external canary")
}

type workspaceHeaderHook struct {
	*httptest.ResponseRecorder
	hook func()
}

func (w *workspaceHeaderHook) Header() http.Header {
	if w.hook != nil {
		hook := w.hook
		w.hook = nil
		hook()
	}
	return w.ResponseRecorder.Header()
}

func TestWorkspaceDownloadKeepsOpenedFileAcrossReplacement(t *testing.T) {
	s, dir := workspaceServer(t)
	path := filepath.Join(dir, "download.txt")
	workspacePut(t, path, "inside content")
	outside := t.TempDir()
	canary := filepath.Join(outside, "canary.txt")
	workspacePut(t, canary, "external canary")
	workspaceSymlink(t, canary, filepath.Join(dir, "probe"))
	w := &workspaceHeaderHook{ResponseRecorder: httptest.NewRecorder(), hook: func() {
		if err := os.Rename(path, filepath.Join(dir, "held.txt")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(canary, path); err != nil {
			t.Fatal(err)
		}
	}}
	s.wsDownload(w, httptest.NewRequest(http.MethodGet, "/api/workspace/download?path=download.txt", nil))
	if w.Code != 200 || w.Body.String() != "inside content" {
		t.Fatalf("download after replacement: %d %s", w.Code, w.Body.String())
	}
	workspaceAssertFile(t, canary, "external canary")
}

type workspaceZeroReader struct{}

func (workspaceZeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestWorkspaceUploadRemovesMultipartTempFiles(t *testing.T) {
	s, dir := workspaceServer(t)
	r := workspaceUploadRequest(t, "", "large.bin", io.LimitReader(workspaceZeroReader{}, 33<<20))
	w := httptest.NewRecorder()
	s.wsUpload(w, r)
	if w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if fi, err := os.Stat(filepath.Join(dir, "large.bin")); err != nil || fi.Size() != 33<<20 {
		t.Fatalf("uploaded size: %v, %v", fi, err)
	}
	if r.MultipartForm == nil || len(r.MultipartForm.File["file"]) != 1 {
		t.Fatal("missing parsed upload")
	}
	if file, err := r.MultipartForm.File["file"][0].Open(); err == nil {
		file.Close()
		t.Fatal("multipart spill file remains after upload")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("multipart spill cleanup: %v", err)
	}
}

func TestWorkspaceReadSizeAndBinaryLimits(t *testing.T) {
	s, dir := workspaceServer(t)
	workspacePut(t, filepath.Join(dir, "binary"), "a\x00b")
	workspacePut(t, filepath.Join(dir, "large"), strings.Repeat("a", maxWorkspaceRead+1))
	for _, filename := range []string{"binary", "large"} {
		w := workspaceRequest(t, s.wsRead, http.MethodGet, filename, "")
		var result struct {
			Binary   bool    `json:"binary"`
			TooLarge bool    `json:"too_large"`
			Content  *string `json:"content"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || !result.Binary || result.Content != nil || result.TooLarge != (filename == "large") {
			t.Fatalf("read limit %s: %d %s (%v)", filename, w.Code, w.Body.String(), err)
		}
	}
}
