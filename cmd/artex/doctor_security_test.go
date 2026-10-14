package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorOptionalSecurityToolsNeverExecute(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	for _, name := range []string{"nuclei", "vulnx"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", root)
	t.Setenv("ARTEX_CONFIG", filepath.Join(root, "absent.json"))
	t.Setenv("ARTEX_PG_DSN", "invalid fixture")
	t.Setenv("ARTEX_SKILL_DIR", root)
	t.Setenv("NUCLEI_TEMPLATES_DIR", root)
	report := inspectDoctor(root)
	for _, name := range []string{"nuclei", "vulnx"} {
		found := false
		for _, check := range report.Checks {
			if check.Name == name {
				found = true
				if check.Status != "OK" || !strings.Contains(check.Detail, "execution not tested") {
					t.Fatalf("%+v", check)
				}
			}
		}
		if !found {
			t.Fatal("missing tool check", name)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("doctor executed a tool")
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("NUCLEI_TEMPLATES_DIR", "")
	for _, check := range inspectDoctor(root).Checks {
		if (check.Name == "nuclei" || check.Name == "vulnx") && check.Status != "WARN" {
			t.Fatalf("missing optional tool: %+v", check)
		}
		if check.Name == "optional nuclei templates directory" && check.Status != "SKIP" {
			t.Fatalf("unknown templates: %+v", check)
		}
	}
}
