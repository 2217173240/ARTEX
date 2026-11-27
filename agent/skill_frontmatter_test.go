package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/skill"
)

func TestSkillFrontmatterLoadsAllBuiltinSkills(t *testing.T) {
	skillsDir := filepath.Join("..", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", skillsDir, err)
	}
	var expectedDirs int
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(skillsDir, e.Name(), "SKILL.md")); err == nil {
				expectedDirs++
			}
		}
	}
	if expectedDirs == 0 {
		t.Fatalf("no SKILL.md directories found under %q", skillsDir)
	}

	reg, err := skill.LoadDir(skillsDir)
	if err != nil {
		t.Fatalf("skill.LoadDir(%q): %v", skillsDir, err)
	}
	loaded := reg.List()
	if len(loaded) != expectedDirs {
		t.Fatalf("loaded %d skills, want %d", len(loaded), expectedDirs)
	}
	for _, s := range loaded {
		if strings.TrimSpace(s.Name) == "" {
			t.Errorf("skill in %q loaded with empty Name", s.Dir)
		}
		if strings.TrimSpace(s.Description) == "" {
			t.Errorf("skill %q (%q) loaded with empty Description — check YAML frontmatter", s.Name, s.Dir)
		}
		if strings.TrimSpace(s.Instructions) == "" {
			t.Errorf("skill %q (%q) loaded with empty Instructions body", s.Name, s.Dir)
		}
	}

	ss, ok := reg.Get("scopesentry-mcp")
	if !ok {
		t.Fatalf("expected scopesentry-mcp skill to be registered; got %v", loaded)
	}
	if !strings.Contains(ss.Instructions, "# ScopeSentry MCP 使用指南") {
		t.Fatalf("scopesentry-mcp missing title in Instructions")
	}
	if !strings.Contains(ss.Instructions, "## 1. 准备工作") {
		t.Fatalf("scopesentry-mcp missing section 1 (## 1. 准备工作) in Instructions")
	}
}
