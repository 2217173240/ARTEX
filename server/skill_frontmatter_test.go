package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func decodeSkillTestFrontmatter(t *testing.T, content []byte) map[string]any {
	t.Helper()
	start := bytes.IndexByte(content, '\n') + 1
	end := bytes.Index(content[start:], []byte("\n---"))
	if start == 0 || end < 0 {
		t.Fatalf("missing frontmatter fences: %q", content)
	}
	var fields map[string]any
	if err := yaml.Unmarshal(content[start:start+end], &fields); err != nil {
		t.Fatalf("invalid rewritten frontmatter: %v\n%s", err, content)
	}
	return fields
}

func TestRewriteSkillFrontmatterPreservesYAMLAndBody(t *testing.T) {
	body := "\n# Instructions\n\nKeep: colons and \"quotes\".\n---\n```yaml\nkey: value\n```\nlast line"
	original := []byte(`---
name: sample
# A metadata comment.
description: >-
  First: quoted "text"
  second line.
license: 'License: custom'
compatibility: "requires: tools"
mcps: [browser, scanner]
mcp: legacy
quoted: 'value: with "quotes"'
multiline: |
  ---
  command: "argument"
  next line
unknown:
  sequence:
    - name: 'entry: one'
      options: [true, 2, null, "a: b"]
    - [nested, sequence]
  nested:
    enabled: false
    count: 7
anchor: &config {nested: [a, b]}
reference: *config
---` + body)
	before := decodeSkillTestFrontmatter(t, original)
	description := "Changed: \"quoted\" value\nsecond line"
	license := ""
	compatibility := "requires: tool >= 2"
	mcps := []string{" browser ", "", "tool: custom", "quoted \"tool\""}
	updated, err := rewriteSkillFrontmatter(original, &mcps, &description, &license, &compatibility)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(updated, []byte("---"+body)) {
		t.Fatalf("instruction body changed: %q", updated)
	}
	if !bytes.Contains(updated, []byte("# A metadata comment.")) {
		t.Fatal("metadata comment lost")
	}
	after := decodeSkillTestFrontmatter(t, updated)
	before["description"] = description
	before["license"] = license
	before["compatibility"] = compatibility
	before["mcps"] = []any{"browser", "tool: custom", "quoted \"tool\""}
	delete(before, "mcp")
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("metadata changed unexpectedly:\ngot %#v\nwant %#v\n%s", after, before, updated)
	}
}

func TestRewriteSkillFrontmatterNilFields(t *testing.T) {
	original := []byte("---\nname: sample\ndescription: |\n  original: description\n  another line\nlicense: 'License: custom'\ncompatibility: [one, two]\nmcp: legacy\nmcps: [existing]\n---\nInstructions")
	updated, err := rewriteSkillFrontmatter(original, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(updated, original) {
		t.Fatalf("omitted fields changed content: %q", updated)
	}
	license := "updated: license"
	updated, err = rewriteSkillFrontmatter(original, nil, nil, &license, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := decodeSkillTestFrontmatter(t, original)
	before["license"] = license
	if after := decodeSkillTestFrontmatter(t, updated); !reflect.DeepEqual(after, before) {
		t.Fatalf("nil fields changed: got %#v, want %#v", after, before)
	}
}

func TestRewriteSkillFrontmatterClearsMCPAliases(t *testing.T) {
	for _, old := range []string{"mcp: legacy\n", "mcps: [browser]\n", "mcp: legacy\nmcps: browser, scanner\n"} {
		for _, clear := range [][]string{nil, {}, {" ", ""}} {
			original := []byte("---\nname: sample\n" + old + "---\nbody")
			updated, err := rewriteSkillFrontmatter(original, &clear, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			fields := decodeSkillTestFrontmatter(t, updated)
			for _, key := range []string{"mcp", "mcps"} {
				if _, exists := fields[key]; exists {
					t.Fatalf("clear retained %s: %s", key, updated)
				}
			}
		}
	}
}

func TestRewriteSkillFrontmatterPreservesLineEndingsAndFinalBytes(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		for _, body := range []string{"", "\nbody", "\nbody\n", "\r\nbody\r\n\r\n"} {
			original := []byte("---" + newline + "name: sample" + newline + "description: old" + newline + "---" + body)
			description := "new: description"
			updated, err := rewriteSkillFrontmatter(original, nil, &description, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasSuffix(updated, []byte(newline+"---"+body)) {
				t.Fatalf("closing delimiter/body changed (newline %q, body %q): %q", newline, body, updated)
			}
			if newline == "\r\n" {
				header := bytes.SplitN(updated, []byte("---"), 3)[1]
				if bytes.Contains(bytes.ReplaceAll(header, []byte("\r\n"), nil), []byte("\n")) {
					t.Fatalf("frontmatter acquired bare LF: %q", updated)
				}
			}
		}
	}
}

func TestRewriteSkillFrontmatterRejectsInvalidYAML(t *testing.T) {
	cases := map[string]string{
		"no frontmatter":   "# Instructions\nbody",
		"unclosed":         "---\nname: sample\nbody",
		"malformed":        "---\nname: [unclosed\n---\nbody",
		"sequence":         "---\n- name\n- sample\n---\nbody",
		"scalar":           "---\nplain text\n---\nbody",
		"empty":            "---\n---\nbody",
		"duplicate":        "---\nname: sample\nname: second\n---\nbody",
		"nested duplicate": "---\nname: sample\nunknown:\n  nested: 1\n  nested: 2\n---\nbody",
		"nonstring key":    "---\n1: value\n---\nbody",
		"custom key tag":   "---\n!custom name: value\n---\nbody",
		"nonmerge value":   "---\n!!merge wrong: {name: sample}\n---\nbody",
		"unknown alias":    "---\nname: sample\nunknown: *missing\n---\nbody",
		"extra document":   "---\nname: sample\n...\nname: second\n---\nbody",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			original := []byte(text)
			before := bytes.Clone(original)
			description := "updated"
			updated, err := rewriteSkillFrontmatter(original, nil, &description, nil, nil)
			if err == nil || updated != nil {
				t.Fatalf("invalid metadata accepted: updated %q, err %v", updated, err)
			}
			if !bytes.Equal(original, before) {
				t.Fatal("input bytes were mutated on error")
			}
		})
	}
}

func TestRewriteSkillFrontmatterRejectsDanglingMCPAlias(t *testing.T) {
	original := []byte("---\nname: sample\nmcps: &tools [browser]\nunknown: *tools\n---\nbody")
	clear := []string{}
	if updated, err := rewriteSkillFrontmatter(original, &clear, nil, nil, nil); err == nil || updated != nil {
		t.Fatalf("clear created a dangling alias: updated %q, err %v", updated, err)
	}
}

func TestRewriteSkillFrontmatterPreservesYAMLMerge(t *testing.T) {
	original := []byte("---\nname: sample\ndefaults: &defaults\n  mcps: [browser]\n  unknown:\n    options: [one, two]\n<<: *defaults\nlicense: old\n---\nInstructions without trailing newline")
	updated, err := rewriteSkillFrontmatter(original, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("valid YAML merge rejected with omitted fields: %v", err)
	}
	if !bytes.Equal(updated, original) {
		t.Fatalf("omitted fields changed merged YAML: %q", updated)
	}
	license := "Updated: license"
	updated, err = rewriteSkillFrontmatter(original, nil, nil, &license, nil)
	if err != nil {
		t.Fatalf("unrelated update rejected valid YAML merge: %v", err)
	}
	before := decodeSkillTestFrontmatter(t, original)
	before["license"] = license
	if after := decodeSkillTestFrontmatter(t, updated); !reflect.DeepEqual(after, before) {
		t.Fatalf("merged unknown/MCP values changed: got %#v, want %#v", after, before)
	}
	if !bytes.HasSuffix(updated, []byte("---\nInstructions without trailing newline")) {
		t.Fatalf("instruction body changed: %q", updated)
	}
}

func TestRewriteSkillFrontmatterRejectsInheritedMCPClear(t *testing.T) {
	for _, field := range []string{"mcp", "mcps"} {
		original := []byte("---\nname: sample\ndefaults: &defaults\n  " + field + ": [browser]\n<<: *defaults\n---\nbody")
		clear := []string{}
		updated, err := rewriteSkillFrontmatter(original, &clear, nil, nil, nil)
		if err == nil || updated != nil {
			t.Fatalf("clear retained inherited %s: updated %q, err %v", field, updated, err)
		}
		if !strings.Contains(err.Error(), "inherited from a YAML merge") {
			t.Fatalf("clear did not reach inherited MCP check: %v", err)
		}
	}
}

func TestRewriteSkillFrontmatterRejectsChangingSharedAnchor(t *testing.T) {
	original := []byte("---\nname: sample\ndescription: &description original\nunknown: *description\n---\nbody")
	description := "updated"
	if updated, err := rewriteSkillFrontmatter(original, nil, &description, nil, nil); err == nil || updated != nil {
		t.Fatalf("update changed an unknown field through a shared anchor: updated %q, err %v", updated, err)
	}
}

func TestUpdateSkillMetaInvalidYAMLDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "sample")
	if err := os.Mkdir(skillPath, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(skillPath, "SKILL.md")
	original := []byte("---\nname: sample\nunknown: [unclosed\n---\nInstructions\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/skills/sample/meta", strings.NewReader(`{"description":"updated","mcps":[]}`))
	req.SetPathValue("name", "sample")
	rr := httptest.NewRecorder()
	(&Server{skillDir: dir}).fsUpdateSkillMeta(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatal("invalid SKILL.md was modified")
	}
}
