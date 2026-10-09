package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorConfigAndNoFilesystemMutation(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"database":{"dsn":"invalid secret-password"},"skill_dir":"`+filepath.Join(root, "skills")+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_CONFIG", cfg)
	t.Setenv("ARTEX_PG_DSN", "invalid secret-password")
	t.Setenv("ARTEX_SKILL_DIR", "")
	data := filepath.Join(root, "data")
	var out bytes.Buffer
	if code := runDoctor([]string{"-json", "-data", data}, &out); code != 1 {
		t.Fatalf("code %d", code)
	}
	var r doctorReport
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "secret-password") || strings.Contains(out.String(), cfg) {
		t.Fatal("output exposes configuration")
	}
	for _, p := range []string{data, filepath.Join(root, "skills")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("doctor created %s", p)
		}
	}
	contents, err := os.ReadDir(root)
	if err != nil || len(contents) != 1 {
		t.Fatalf("unexpected files: %v %v", contents, err)
	}
}

func TestDoctorMalformedConfigIsCritical(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"password":"do-not-print",`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_CONFIG", cfg)
	t.Setenv("ARTEX_PG_DSN", "")
	t.Setenv("ARTEX_SKILL_DIR", root)
	r := inspectDoctor(root)
	if r.Checks[0].Status != "FAIL" {
		t.Fatalf("%+v", r)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "do-not-print") {
		t.Fatal("secret exposed")
	}
}

func TestDoctorDirectoryDoesNotClaimWritability(t *testing.T) {
	var c doctorCheck
	checkDoctorDir(t.TempDir(), "data", func(s, n, d string) { c = doctorCheck{n, s, d} })
	if c.Status != "OK" || !strings.Contains(c.Detail, "writability not tested") {
		t.Fatalf("%+v", c)
	}
}

func TestDoctorDispatchDoesNotParseServerFlags(t *testing.T) {
	previous := os.Args
	defer func() { os.Args = previous }()
	os.Args = []string{"artex", "doctor", "-unknown-secret-value"}
	if code := run(); code != 2 {
		t.Fatalf("code %d", code)
	}
}

func TestDoctorUsageDoesNotEchoSecrets(t *testing.T) {
	var out bytes.Buffer
	if runDoctor([]string{"-password=secret"}, &out) != 2 || strings.Contains(out.String(), "secret") {
		t.Fatalf("%s", out.String())
	}
}

func TestDoctorDatabaseErrorsAreRedacted(t *testing.T) {
	var r doctorReport
	probeDoctorDatabase(context.Background(), "postgres://user:secret@127.0.0.1:1/unavailable?connect_timeout=1", func(s, n, d string) { r.Checks = append(r.Checks, doctorCheck{n, s, d}) })
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "postgres://") {
		t.Fatalf("%s", b)
	}
	if len(r.Checks) != 2 || r.Checks[0].Status != "FAIL" {
		t.Fatalf("%+v", r)
	}
}
