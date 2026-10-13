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
