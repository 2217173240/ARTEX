package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSkillPathCanonicalSlashes(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"SKILL.md", "SKILL.md"},
		{"scripts/run.py", "scripts/run.py"},
		{"./参考/./子目录/中文说明.md", "参考/子目录/中文说明.md"},
		{"references/a b.txt", "references/a b.txt"},
		{"参考/子目录/", "参考/子目录"},
		{strings.Repeat("a", maxSkillPathLen), strings.Repeat("a", maxSkillPathLen)},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, msg := skillRelPath(tc.input)
			if msg != "" || got != tc.want {
				t.Fatalf("skillRelPath(%q) = (%q, %q), want (%q, empty error)", tc.input, got, msg, tc.want)
			}
		})
	}
}

func TestSkillPathRejectsUnsafeInputs(t *testing.T) {
	for _, input := range []string{
		"", ".", "./", "../outside", "a/../../b", "a..b", "/absolute", "//host/share",
		"a//b", `a\b`, `C:\skills\file.md`, "C:/skills/file.md", "C:file.md", `\\host\share\file.md`,
		"%2e%2e/file.md", "%252e%252e/file.md", "a:b", "a#b", "a?b", "a*b", `a"b`, "a<b", "a>b", "a|b",
		"a\x00b", "a\nb", "a\x7fb", "中文\u00a0名.md", "中\u202e文.md", "a\ue000b", "a\ufffdb",
		string([]byte{0xff}), strings.Repeat("a", maxSkillPathLen+1),
	} {
		t.Run(input, func(t *testing.T) {
			if got, msg := skillRelPath(input); got != "" || msg == "" {
				t.Fatalf("skillRelPath(%q) = (%q, %q), want rejection", input, got, msg)
			}
		})
	}
}

// Exercise the file handlers without creating a Manager or opening PostgreSQL.
func skillPathRequest(t *testing.T, handler http.HandlerFunc, method, file string, body any, status int) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, "/", bytes.NewReader(data))
	r.SetPathValue("name", "测试技能")
	r.SetPathValue("file", file)
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != status {
		t.Fatalf("%s %q: status %d, want %d; body %s", method, file, w.Code, status, w.Body.String())
	}
	return w
}

func TestSkillPathFilesystemRoundtrip(t *testing.T) {
	s := &Server{skillDir: t.TempDir()}
	root := filepath.Join(s.skillDir, "测试技能")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("synthetic skill"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := skillPathRequest(t, s.fsCreateDir, http.MethodPost, "", map[string]string{"path": "参考/子目录/"}, http.StatusCreated)
	var created struct{ Dir string }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Dir != "参考/子目录" {
		t.Fatalf("created directory = %q, error %v; want slash path", created.Dir, err)
	}
	skillPathRequest(t, s.fsCreateDir, http.MethodPost, "", map[string]string{"path": "空目录/"}, http.StatusCreated)
	skillPathRequest(t, s.fsWriteFile, http.MethodPut, "./参考/子目录/中文说明.md", map[string]string{"content": "original"}, http.StatusOK)

	// Literal slash expectations catch native Windows separator regressions.
	want := []string{"SKILL.md", "参考/", "参考/子目录/", "参考/子目录/中文说明.md", "空目录/"}
	entries, err := walkSkillFiles(root)
	if err != nil || !reflect.DeepEqual(entries, want) {
		t.Fatalf("walkSkillFiles = %q, error %v; want %q", entries, err, want)
	}
	w = skillPathRequest(t, s.fsListFiles, http.MethodGet, "", nil, http.StatusOK)
	var listed struct{ Files []string }
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || !reflect.DeepEqual(listed.Files, want) {
		t.Fatalf("listed files = %q, error %v; want %q", listed.Files, err, want)
	}
	for _, entry := range listed.Files {
		clean, msg := skillRelPath(entry)
		if msg != "" || clean != strings.TrimSuffix(entry, "/") {
			t.Fatalf("listed path %q does not roundtrip: (%q, %q)", entry, clean, msg)
		}
		if strings.HasSuffix(entry, "/") {
			continue
		}
		w = skillPathRequest(t, s.fsReadFile, http.MethodGet, entry, nil, http.StatusOK)
		var read struct{ File, Content string }
		if err := json.Unmarshal(w.Body.Bytes(), &read); err != nil || read.File != entry {
			t.Fatalf("read listed path %q: file %q, error %v", entry, read.File, err)
		}
		wantContent := "synthetic skill"
		if entry == "参考/子目录/中文说明.md" {
			wantContent = "original"
		}
		if read.Content != wantContent {
			t.Fatalf("read %q = %q, want %q", entry, read.Content, wantContent)
		}
	}

	file := listed.Files[3]
	skillPathRequest(t, s.fsWriteFile, http.MethodPut, file, map[string]string{"content": "updated"}, http.StatusOK)
	data, err := os.ReadFile(filepath.Join(root, "参考", "子目录", "中文说明.md"))
	if err != nil || string(data) != "updated" {
		t.Fatalf("updated file = %q, error %v", data, err)
	}
	w = skillPathRequest(t, s.fsDeletePath, http.MethodDelete, file, nil, http.StatusOK)
	var deleted struct{ Deleted string }
	if err := json.Unmarshal(w.Body.Bytes(), &deleted); err != nil || deleted.Deleted != file {
		t.Fatalf("deleted path = %q, error %v; want %q", deleted.Deleted, err, file)
	}
	skillPathRequest(t, s.fsReadFile, http.MethodGet, file, nil, http.StatusNotFound)
	w = skillPathRequest(t, s.fsDeletePath, http.MethodDelete, listed.Files[2], nil, http.StatusOK)
	if err := json.Unmarshal(w.Body.Bytes(), &deleted); err != nil || deleted.Deleted != "参考/子目录" {
		t.Fatalf("deleted directory = %q, error %v; want slash path", deleted.Deleted, err)
	}
	if _, err := os.Stat(filepath.Join(root, "参考", "子目录")); !os.IsNotExist(err) {
		t.Fatalf("deleted directory still exists: %v", err)
	}

	// A write to a new nested file still creates native parent directories.
	skillPathRequest(t, s.fsWriteFile, http.MethodPut, "资料/示例.md", map[string]string{"content": "new"}, http.StatusOK)
	data, err = os.ReadFile(filepath.Join(root, "资料", "示例.md"))
	if err != nil || string(data) != "new" {
		t.Fatalf("new nested file = %q, error %v", data, err)
	}
	// Validation still reports the existing HTTP error contract.
	skillPathRequest(t, s.fsReadFile, http.MethodGet, `参考\中文说明.md`, nil, http.StatusBadRequest)
	skillPathRequest(t, s.fsWriteFile, http.MethodPut, "../outside", map[string]string{"content": "blocked"}, http.StatusBadRequest)
	skillPathRequest(t, s.fsCreateDir, http.MethodPost, "", map[string]string{"path": "C:/outside"}, http.StatusBadRequest)
	skillPathRequest(t, s.fsDeletePath, http.MethodDelete, "%2e%2e/outside", nil, http.StatusBadRequest)
}
