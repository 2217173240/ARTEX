package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func chatUploadRequest(t *testing.T, scope, filename string, data io.Reader) *http.Request {
	t.Helper()
	r := workspaceUploadRequest(t, "", filename, data)
	r.URL = &url.URL{Path: "/api/chat/upload", RawQuery: "scope=" + scope + "&id=fixture"}
	return r
}

func chatUploadAttachments(t *testing.T, w *httptest.ResponseRecorder) []chatAttachment {
	t.Helper()
	var result struct {
		Attachments []chatAttachment `json:"attachments"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || len(result.Attachments) != 1 {
		t.Fatalf("chat upload: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	return result.Attachments
}

func TestChatUploadRejectsEscapingDirectories(t *testing.T) {
	for scope, sub := range map[string]string{"session": "sessions", "staging": "drafts"} {
		for _, level := range []string{"sub", "id", "uploads"} {
			t.Run(scope+"/"+level, func(t *testing.T) {
				s, dir := workspaceServer(t)
				outside := t.TempDir()
				workspacePut(t, filepath.Join(outside, "canary.txt"), "external canary")
				link := filepath.Join(dir, sub)
				if level == "id" || level == "uploads" {
					link = filepath.Join(link, "fixture")
				}
				if level == "uploads" {
					link = filepath.Join(link, "uploads")
				}
				if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
					t.Fatal(err)
				}
				workspaceSymlink(t, outside, link)
				w := httptest.NewRecorder()
				s.chatUpload(w, chatUploadRequest(t, scope, "canary.txt", strings.NewReader("overwritten")))
				if w.Code < 400 {
					t.Fatalf("escaping chat upload returned %d: %s", w.Code, w.Body.String())
				}
				workspaceAssertFile(t, filepath.Join(outside, "canary.txt"), "external canary")
				entries, err := os.ReadDir(outside)
				if err != nil || len(entries) != 1 {
					t.Fatalf("outside changed: %+v, %v", entries, err)
				}
			})
		}
	}
}

func TestChatUploadKeepsExistingFilesAndFinalSymlinks(t *testing.T) {
	for scope, sub := range map[string]string{"session": "sessions", "staging": "drafts"} {
		t.Run(scope, func(t *testing.T) {
			s, dir := workspaceServer(t)
			uploadDir := filepath.Join(dir, sub, "fixture", "uploads")
			if err := os.MkdirAll(uploadDir, 0o755); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			canary := filepath.Join(outside, "canary.txt")
			workspacePut(t, canary, "external canary")
			workspaceSymlink(t, canary, filepath.Join(uploadDir, "report.txt"))
			workspacePut(t, filepath.Join(uploadDir, "report-1.txt"), "existing attachment")
			w := httptest.NewRecorder()
			s.chatUpload(w, chatUploadRequest(t, scope, "../report.txt", strings.NewReader("new attachment")))
			att := chatUploadAttachments(t, w)[0]
			if att.Name != "report-2.txt" || att.Path != "uploads/report-2.txt" || att.Size != int64(len("new attachment")) || att.Abs != filepath.Join(uploadDir, "report-2.txt") {
				t.Fatalf("attachment API changed: %+v", att)
			}
			workspaceAssertFile(t, canary, "external canary")
			workspaceAssertFile(t, filepath.Join(uploadDir, "report-1.txt"), "existing attachment")
			workspaceAssertFile(t, att.Abs, "new attachment")
		})
	}
}

func TestChatUploadConcurrentNamesDoNotClobber(t *testing.T) {
	s, dir := workspaceServer(t)
	const count = 12
	requests := make([]*http.Request, count)
	for i := range requests {
		requests[i] = chatUploadRequest(t, "session", "report.txt", strings.NewReader(strings.Repeat("x", i+1)))
	}
	type result struct {
		index int
		w     *httptest.ResponseRecorder
	}
	results := make(chan result, count)
	start := make(chan struct{})
	for i, request := range requests {
		go func(index int, r *http.Request) {
			<-start
			w := httptest.NewRecorder()
			s.chatUpload(w, r)
			results <- result{index, w}
		}(i, request)
	}
	close(start)
	names := make(map[string]bool, count)
	for i := 0; i < count; i++ {
		result := <-results
		att := chatUploadAttachments(t, result.w)[0]
		if names[att.Name] {
			t.Fatalf("uploads reserved same filename %q", att.Name)
		}
		names[att.Name] = true
		workspaceAssertFile(t, att.Abs, strings.Repeat("x", result.index+1))
	}
	entries, err := os.ReadDir(filepath.Join(dir, "sessions", "fixture", "uploads"))
	if err != nil || len(entries) != count {
		t.Fatalf("concurrent upload directory: %d files, %v", len(entries), err)
	}
}

func TestChatUploadKeepsOpenedRootAcrossReplacement(t *testing.T) {
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
	probe := filepath.Join(parent, "symlink-probe")
	workspaceSymlink(t, outside, probe)
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{dir: dir}}
	r := chatUploadRequest(t, "staging", "canary.txt", strings.NewReader("uploaded inside"))
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
	s.chatUpload(w, r)
	chatUploadAttachments(t, w)
	if !body.closed {
		t.Fatal("upload body remains open")
	}
	workspaceAssertFile(t, filepath.Join(outside, "canary.txt"), "external canary")
	workspaceAssertFile(t, filepath.Join(held, "drafts", "fixture", "uploads", "canary.txt"), "uploaded inside")
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 1 {
		t.Fatalf("outside changed: %+v, %v", entries, err)
	}
}
