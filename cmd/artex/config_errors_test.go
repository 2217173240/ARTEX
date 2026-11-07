package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestDoctorConfigFailureDiagnosis(t *testing.T) {
	for _, kind := range []string{"syntax", "type", "read", "missing"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config.json")
			switch kind {
			case "syntax":
				if err := os.WriteFile(path, []byte(`{"password":"private-marker",`), 0600); err != nil {
					t.Fatal(err)
				}
			case "type":
				if err := os.WriteFile(path, []byte(`{"database":{"port":"private-marker"}}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "read":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("ARTEX_CONFIG", path)
			t.Setenv("ARTEX_HOME", root)
			t.Setenv("ARTEX_PG_DSN", "")
			t.Setenv("ARTEX_SKILL_DIR", root)
			var out bytes.Buffer
			if code := runDoctor([]string{"-json", "-data", root}, &out); code != 1 {
				t.Fatalf("code %d", code)
			}
			var report doctorReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "private-marker") {
				t.Fatal("configuration value disclosed")
			}
			foundDB, foundOptional := false, false
			for _, check := range report.Checks {
				if check.Name == "database" {
					foundDB = true
					if kind == "missing" {
						if !strings.Contains(check.Detail, "no database connection configured") {
							t.Fatal(check.Detail)
						}
					} else if !strings.Contains(check.Detail, path) || strings.Contains(check.Detail, "no database connection configured") {
						t.Fatal(check.Detail)
					}
				}
				if check.Name == "optional skill asset api-recon/SKILL.md" {
					foundOptional = true
				}
			}
			if !foundDB || !foundOptional {
				t.Fatal("independent checks missing")
			}
		})
	}
}

func TestLauncherConfigFailureRepairPreservesFile(t *testing.T) {
	for _, kind := range []string{"syntax", "read"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "config.json")
			original := []byte(`{"password":"private-marker",`)
			if kind == "read" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ARTEX_CONFIG", path)
			t.Setenv("ARTEX_HOME", home)
			t.Setenv("ARTEX_PG_DSN", "")
			done := make(chan int, 1)
			go func() { done <- launcherServe(home, io.Discard, io.Discard) }()
			var state launcherState
			for i := 0; i < 100; i++ {
				b, _ := os.ReadFile(filepath.Join(home, "launcher.json"))
				if json.Unmarshal(b, &state) == nil && state.Address != "" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if state.Address == "" {
				t.Fatal("launcher did not start")
			}
			defer func() {
				resp, err := launcherRequest(state, "POST", "/stop")
				if err != nil {
					t.Error(err)
					return
				}
				resp.Body.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("launcher did not stop")
				}
			}()
			resp, err := launcherRequest(state, "GET", "/status")
			if err != nil {
				t.Fatal(err)
			}
			var status launcherStatus
			err = json.NewDecoder(resp.Body).Decode(&status)
			resp.Body.Close()
			if err != nil || status.Phase != "failed" || !strings.Contains(status.Message, path) || strings.Contains(status.Message, "private-marker") {
				t.Fatalf("unsafe repair status: %+v (%v)", status, err)
			}
			resp, err = http.Get("http://" + state.Address + "/")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), `name="host"`) || strings.Contains(string(body), "private-marker") || !strings.Contains(string(body), path) {
				t.Fatal("repair page mismatch")
			}
			nonce := regexp.MustCompile(`name="nonce" value="([a-f0-9]+)"`).FindSubmatch(body)
			if len(nonce) != 2 {
				t.Fatal("stop control missing")
			}
			values := url.Values{"nonce": {string(nonce[1])}, "host": {"localhost"}, "port": {"5432"}, "user": {"u"}, "database": {"d"}}
			req, _ := http.NewRequest("POST", "http://"+state.Address+"/", strings.NewReader(values.Encode()))
			req.Header.Set("Origin", "http://"+state.Address)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			resp, err = http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("repair accepted setup: %d", resp.StatusCode)
			}
			if kind == "syntax" {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(after, original) {
					t.Fatal("configuration changed")
				}
			} else if info, err := os.Stat(path); err != nil || !info.IsDir() {
				t.Fatal("configuration directory changed")
			}
		})
	}
}
