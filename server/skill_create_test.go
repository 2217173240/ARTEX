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
	"sync"
	"testing"

	"github.com/Autumn-27/norma/skill"
)

func TestCreateSkillFrontmatterRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, description, license, compatibility string
		mcps                                      []string
	}{
		{"embedded-colon", "target: embedded", "yes", "tools: available", []string{" browser ", "", "scanner"}},
		{"multiline-metadata", "first line\n---\nmcps: [injected]\nlast line", "true", "null", []string{"tool, literal", "---\nlicense: injected", "工具: 中文"}},
		{"中文技能", "  中文描述: 保留空格\n第二行  ", "  许可证: 自定义  ", "requires [tools] # note", []string{" 中文工具 ", "quoted \"tool\"", "yes", "true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			instructions := "\n# Instructions\n\nKeep: colon, quotes, and body delimiters.\n---\n```yaml\nkey: value\n```\nlast line without newline"
			request, err := json.Marshal(map[string]any{
				"name": tc.name, "description": tc.description, "license": tc.license,
				"compatibility": tc.compatibility, "mcps": tc.mcps, "instructions": instructions,
			})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			(&Server{skillDir: dir}).fsCreateSkill(rec, httptest.NewRequest(http.MethodPost, "/api/skills", bytes.NewReader(request)))
			if rec.Code != http.StatusCreated {
				t.Fatalf("create status=%d body=%s", rec.Code, rec.Body)
			}
			content, err := os.ReadFile(filepath.Join(dir, tc.name, "SKILL.md"))
			if err != nil {
				t.Fatal(err)
			}
			fields := decodeSkillTestFrontmatter(t, content)
			for field, want := range map[string]string{"name": tc.name, "description": tc.description, "license": tc.license, "compatibility": tc.compatibility} {
				if got, ok := fields[field].(string); !ok || got != want {
					t.Fatalf("%s lost string value: got %#v want %q\n%s", field, fields[field], want, content)
				}
			}
			if !bytes.HasSuffix(content, []byte("---\n"+instructions)) {
				t.Fatalf("Markdown instructions changed: %q", content)
			}
			registry, err := skill.LoadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			loaded := registry.List()
			if len(loaded) != 1 {
				t.Fatalf("loaded %d skills", len(loaded))
			}
			got := loaded[0]
			if got.Name != tc.name || got.Description != tc.description || got.License != tc.license || got.Compatibility != tc.compatibility || !reflect.DeepEqual(got.MCPs, cleanStrs(tc.mcps)) || got.Instructions != strings.TrimSpace(instructions) {
				t.Fatalf("real skill loader lost fields: %+v; want description=%q license=%q compatibility=%q mcps=%q", got, tc.description, tc.license, tc.compatibility, cleanStrs(tc.mcps))
			}
		})
	}
}

func TestCreateSkillFrontmatterDefaultsAndConflict(t *testing.T) {
	dir := t.TempDir()
	s := &Server{skillDir: dir}
	create := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.fsCreateSkill(rec, httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(`{"name":"scaffold","description":"simple","mcps":[" ",""],"instructions":"  "}`)))
		return rec
	}
	if rec := create(); rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body)
	}
	path := filepath.Join(dir, "scaffold", "SKILL.md")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fields := decodeSkillTestFrontmatter(t, content)
	if !reflect.DeepEqual(fields, map[string]any{"name": "scaffold", "description": "simple"}) {
		t.Fatalf("optional empty fields not omitted: %#v", fields)
	}
	if !bytes.HasSuffix(content, []byte("---\n## scaffold\n\n<!-- Describe step-by-step instructions in Markdown. -->\n\n1. \n2. \n3. \n")) {
		t.Fatalf("default instruction scaffold changed: %q", content)
	}
	if rec := create(); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate status=%d body=%s", rec.Code, rec.Body)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, content) {
		t.Fatalf("duplicate request rewrote existing skill: %v", err)
	}
}

func TestCreateSkillConcurrentRequestsPreserveWinner(t *testing.T) {
	// Both requests start together; only the creator of the directory may
	// write its skill. Repeated pairs exercise the previous Stat/MkdirAll race.
	for range 32 {
		dir := t.TempDir()
		s := &Server{skillDir: dir}
		var responses [2]*httptest.ResponseRecorder
		var requests [2][]byte
		for i, description := range []string{"winner: first", "winner: second"} {
			var err error
			requests[i], err = json.Marshal(map[string]string{"name": "concurrent", "description": description, "instructions": description})
			if err != nil {
				t.Fatal(err)
			}
		}
		start := make(chan struct{})
		var workers sync.WaitGroup
		for i := range requests {
			workers.Go(func() {
				<-start
				responses[i] = httptest.NewRecorder()
				s.fsCreateSkill(responses[i], httptest.NewRequest(http.MethodPost, "/api/skills", bytes.NewReader(requests[i])))
			})
		}
		close(start)
		workers.Wait()
		winner := -1
		for i, response := range responses {
			switch response.Code {
			case http.StatusCreated:
				if winner != -1 {
					t.Fatalf("both requests succeeded: %s / %s", responses[0].Body, responses[1].Body)
				}
				winner = i
			case http.StatusConflict:
			default:
				t.Fatalf("unexpected create response: %d %s", response.Code, response.Body)
			}
		}
		if winner == -1 {
			t.Fatal("neither request created the skill")
		}
		var expected map[string]string
		if err := json.Unmarshal(requests[winner], &expected); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(filepath.Join(dir, "concurrent", "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		fields := decodeSkillTestFrontmatter(t, content)
		if fields["description"] != expected["description"] || !bytes.HasSuffix(content, []byte("---\n"+expected["instructions"])) {
			t.Fatalf("losing request overwrote winning content: %s", content)
		}
	}
}

func TestCreateSkillPreservesExistingDirectoryAndFiles(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(existing, "unrelated.txt")
	original := []byte("preserve existing data")
	if err := os.WriteFile(unrelated, original, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	(&Server{skillDir: dir}).fsCreateSkill(rec, httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(`{"name":"existing","description":"new"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("existing directory: %d %s", rec.Code, rec.Body)
	}
	after, err := os.ReadFile(unrelated)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("existing file changed: %q %v", after, err)
	}
	if _, err := os.Stat(filepath.Join(existing, "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("existing directory acquired a skill file: %v", err)
	}
}

func TestCreateSkillPreservesExistingSymlink(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(dir, "existing")
	workspaceSymlink(t, outside, path)
	rec := httptest.NewRecorder()
	(&Server{skillDir: dir}).fsCreateSkill(rec, httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(`{"name":"existing","description":"new"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("existing symlink: %d %s", rec.Code, rec.Body)
	}
	if target, err := os.Readlink(path); err != nil || target != outside {
		t.Fatalf("existing symlink changed: %q %v", target, err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("create wrote outside skill root: %v %v", entries, err)
	}
}

func TestCreateSkillCreatesMissingRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "parent", "skills")
	rec := httptest.NewRecorder()
	(&Server{skillDir: dir}).fsCreateSkill(rec, httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(`{"name":"new","description":"new"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("missing root: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, "new", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}
