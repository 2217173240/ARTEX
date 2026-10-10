package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/config"
)

func TestLauncherConfigPrivateExclusive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "config.json")
	if e := launcherWriteConfig(p, "postgres://fixture:password@localhost/test"); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	var c config.Config
	if json.Unmarshal(b, &c) != nil || c.Database.DSN == "" {
		t.Fatal("missing DSN")
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	if e := launcherWriteConfig(p, "replacement"); e == nil {
		t.Fatal("overwrote config")
	}
	after, _ := os.ReadFile(p)
	if string(after) != string(b) {
		t.Fatal("changed existing file")
	}
}

func TestLauncherDSNSerializationAndTLS(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "[::1]", "db.example.com"} {
		form := launcherForm{Host: host, Port: "5432", User: "u@ser:/#", Database: "db /?#%", TLS: "auto"}
		password := "p@ss:/?&#+ %"
		dsn, e := launcherDSN(form, password)
		if e != nil {
			t.Fatal(e)
		}
		cfg, e := pgx.ParseConfig(dsn)
		if e != nil {
			t.Fatal(e)
		}
		if cfg.User != form.User || cfg.Password != password || cfg.Database != form.Database {
			t.Fatal("credential serialization changed values")
		}
		parsed, e := url.Parse(dsn)
		if e != nil {
			t.Fatal(e)
		}
		want := "disable"
		if host == "db.example.com" {
			want = "verify-full"
		}
		if parsed.Query().Get("sslmode") != want {
			t.Fatalf("%s TLS mismatch", host)
		}
	}
	if _, e := launcherDSN(launcherForm{Host: "db.example.com", Port: "5432", User: "u", Database: "d", TLS: "disable"}, "p"); e == nil {
		t.Fatal("remote TLS disabled")
	}
}
func TestLauncherPageEscapesFieldsAndShowsOnlyRelevantControls(t *testing.T) {
	data := launcherPageData{Message: "<script>fixture</script>", Phase: "setup", Form: launcherForm{Host: `\"><script>bad</script>`, Port: "5432", User: "fixture", Database: "fixture", TLS: "auto"}, Log: "private-log"}
	var b bytes.Buffer
	if e := launcherPage.Execute(&b, data); e != nil {
		t.Fatal(e)
	}
	html := b.String()
	if strings.Contains(html, "<script>") || strings.Contains(html, "private-log") || strings.Contains(html, `name="dsn"`) {
		t.Fatal("unsafe or obsolete setup output")
	}
	if !strings.Contains(html, `type="password" name="password" autocomplete="off"`) {
		t.Fatal("password input absent or echoed")
	}
	b.Reset()
	data.Phase = "running"
	data.URL = "http://127.0.0.1:1234"
	if e := launcherPage.Execute(&b, data); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(b.String(), `name="host"`) || !strings.Contains(b.String(), `href="http://127.0.0.1:1234"`) {
		t.Fatal("runtime controls mismatch")
	}
}
func TestLauncherSeedPreservesEditsAndSkipsSymlinks(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	os.WriteFile(filepath.Join(src, "a"), []byte("bundled"), 0600)
	os.WriteFile(filepath.Join(dst, "a"), []byte("edited"), 0600)
	os.WriteFile(filepath.Join(src, "b"), []byte("new"), 0600)
	_ = os.Symlink(filepath.Join(src, "b"), filepath.Join(src, "link"))
	if e := launcherSeedSkills(src, dst); e != nil {
		t.Fatal(e)
	}
	a, _ := os.ReadFile(filepath.Join(dst, "a"))
	if string(a) != "edited" {
		t.Fatal("user edit lost")
	}
	b, _ := os.ReadFile(filepath.Join(dst, "b"))
	if string(b) != "new" {
		t.Fatal("not copied")
	}
	if _, e := os.Stat(filepath.Join(dst, "link")); !os.IsNotExist(e) {
		t.Fatal("copied symlink")
	}
}
func TestLauncherRequestLocalAndAuthenticated(t *testing.T) {
	token := "fixture"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing auth")
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	resp, e := launcherRequest(launcherState{strings.TrimPrefix(srv.URL, "http://"), token}, "POST", "/stop")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	for _, a := range []string{"localhost:1234", "example.com:80", "[::1]:1234", "127.0.0.1:12/path"} {
		if r, e := launcherRequest(launcherState{a, token}, "GET", "/status"); e == nil {
			r.Body.Close()
			t.Fatalf("accepted %s", a)
		}
	}
}
func TestLauncherEnvironmentUsesPrivateHome(t *testing.T) {
	t.Setenv("ARTEX_CONFIG", "")
	home := t.TempDir()
	env := launcherEnv(home, []string{"PATH=fixture", "ARTEX_HOME=old", "ARTEX_CONFIG=old", "ARTEX_SKILL_DIR=old"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "=old") || !strings.Contains(joined, "ARTEX_CONFIG="+filepath.Join(home, "config.json")) {
		t.Fatal(joined)
	}
}

func TestLauncherSetupRejectsForeignHostOriginAndNonce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ARTEX_CONFIG", filepath.Join(home, "config.json"))
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
		resp, e := launcherRequest(state, "POST", "/stop")
		if e != nil {
			t.Error(e)
			return
		}
		resp.Body.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("launcher did not stop")
		}
	}()
	for _, test := range []struct{ host, origin, nonce string }{{"evil.invalid", "http://" + state.Address, "fixture"}, {state.Address, "https://evil.invalid", "fixture"}, {state.Address, "http://" + state.Address, "fixture"}} {
		r, _ := http.NewRequest("POST", "http://"+state.Address+"/", strings.NewReader("nonce="+test.nonce+"&host=localhost&port=5432&user=fixture&database=fixture&password=fixture&tls=auto"))
		r.Host = test.host
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Errorf("status %d", resp.StatusCode)
		}
	}
	if _, e := os.Stat(filepath.Join(home, "config.json")); !os.IsNotExist(e) {
		t.Fatal("unauthorized config write")
	}
	resp, e := http.Get("http://" + state.Address + "/")
	if e != nil {
		t.Fatal(e)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	match := regexp.MustCompile(`name="nonce" value="([a-f0-9]+)"`).FindSubmatch(body)
	if len(match) != 2 {
		t.Fatal("setup nonce missing")
	}
	values := url.Values{"nonce": {string(match[1])}, "host": {"localhost"}, "port": {"bad-port"}, "user": {"preserved-user"}, "database": {"preserved-db"}, "password": {"do-not-echo-password"}, "tls": {"auto"}}
	r, _ := http.NewRequest("POST", "http://"+state.Address+"/", strings.NewReader(values.Encode()))
	r.Header.Set("Origin", "http://"+state.Address)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, e = http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "preserved-user") || !strings.Contains(string(body), "preserved-db") || strings.Contains(string(body), "do-not-echo-password") {
		t.Fatal("invalid form did not safely retain nonsensitive fields")
	}
}

func TestLauncherSeedDoesNotFollowUserDirectorySymlink(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	outside := t.TempDir()
	os.Mkdir(filepath.Join(src, "nested"), 0700)
	os.WriteFile(filepath.Join(src, "nested", "new"), []byte("fixture"), 0600)
	if e := os.Symlink(outside, filepath.Join(dst, "nested")); e != nil {
		t.Skip(e)
	}
	if e := launcherSeedSkills(src, dst); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(e) {
		t.Fatal("followed destination directory symlink")
	}
}

func TestLauncherOSLockRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launcher.lock")
	first, e := launcherLock(path)
	if e != nil {
		t.Fatal(e)
	}
	if second, e := launcherLock(path); e == nil {
		second.Close()
		first.Close()
		t.Fatal("duplicate lock acquired")
	}
	first.Close()
	next, e := launcherLock(path)
	if e != nil {
		t.Fatalf("persistent lockfile prevented restart: %v", e)
	}
	next.Close()
}

func TestLauncherStatusDoesNotExposeControlToken(t *testing.T) {
	t.Setenv("ARTEX_HOME", t.TempDir())
	var out bytes.Buffer
	if code := runLauncher([]string{"status"}, &out, io.Discard); code != 1 {
		t.Fatal(code)
	}
	var status launcherStatus
	if json.Unmarshal(out.Bytes(), &status) != nil || status.Phase != "stopped" {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "token") {
		t.Fatal("token exposed")
	}
}

func TestLauncherLockProcessHelper(t *testing.T) {
	path := os.Getenv("ARTEX_TEST_LOCK")
	if path == "" {
		return
	}
	lock, e := launcherLock(path)
	if e != nil {
		os.Exit(2)
	}
	defer lock.Close()
	fmt.Fprintln(os.Stdout, "locked")
	for {
		time.Sleep(time.Hour)
	}
}
func TestLauncherLockReleasedWhenProcessDies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launcher.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLauncherLockProcessHelper$")
	cmd.Env = append(os.Environ(), "ARTEX_TEST_LOCK="+path)
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "locked\n" {
			t.Fatalf("helper: %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper timeout")
	}
	if f, e := launcherLock(path); e == nil {
		f.Close()
		t.Fatal("lock not exclusive")
	}
	cmd.Process.Kill()
	cmd.Wait()
	lock, e := launcherLock(path)
	if e != nil {
		t.Fatal("dead supervisor retained lock:", e)
	}
	lock.Close()
}
