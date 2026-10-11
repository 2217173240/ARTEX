package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

func TestSetupTokenFilePrivateStableAndConcurrent(t *testing.T) {
	dir := t.TempDir()
	key := []byte("fixture-private-key-32-characters")
	token := deriveSetupToken(key)
	if len(token) != 64 {
		t.Fatalf("token length = %d", len(token))
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); errCh <- ensureSetupTokenFile(dir, deriveSetupToken(key)) }()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, setupTokenFilename)
	st, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Fatalf("private file mode: %v %v", st, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != token+"\n" {
		t.Fatalf("delivery copy mismatch: %v", err)
	}
	if err := ensureSetupTokenFile(dir, token); err != nil {
		t.Fatalf("restart: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary delivery files remain: %v %v", entries, err)
	}
}

func TestJWTKeyConcurrentCreationPreservesSinglePrivateKey(t *testing.T) {
	dir := t.TempDir()
	keys := make(chan []byte, 16)
	errCh := make(chan error, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); key, err := loadOrCreateJWTKey(dir, dir); keys <- key; errCh <- err }()
	}
	wg.Wait()
	close(keys)
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first []byte
	for key := range keys {
		if first == nil {
			first = key
		}
		if !bytes.Equal(first, key) {
			t.Fatal("simultaneous first starts received different keys")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, jwtKeyFilename), []byte("malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateJWTKey(dir, dir); err == nil {
		t.Fatal("malformed key replaced")
	}
	got, _ := os.ReadFile(filepath.Join(dir, jwtKeyFilename))
	if string(got) != "malformed" {
		t.Fatal("malformed existing key changed")
	}
}

func TestAuthInitConcurrentOwnershipPG(t *testing.T) {
	dsn := os.Getenv("ARTEX_AUTH_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated ARTEX_AUTH_TEST_DSN not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if _, exists, err := pg.GetSetting(authPassKey); err != nil || exists {
		t.Fatalf("fixture must have no admin password: exists=%v err=%v", exists, err)
	}
	defer func() { _, _ = pg.Exec("DELETE FROM settings WHERE key=$1", authPassKey) }()
	key := []byte("fixture-private-key-32-characters")
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s := &Server{m: &Manager{pg: pg}, jwtKey: key, setupToken: deriveSetupToken(key)}
			results <- authCredentialRequest(s, "init", `{"password":"fixture-admin-password","setup_token":"`+s.setupToken+`"}`, "", nil)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for w := range results {
		switch w.Code {
		case 200:
			successes++
		case 403:
			if len(w.Result().Cookies()) != 0 {
				t.Fatal("losing initialization issued a session")
			}
		default:
			t.Fatalf("unexpected concurrent response %d %s", w.Code, w.Body.String())
		}
	}
	if successes != 1 {
		t.Fatalf("successful initializations=%d want 1", successes)
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil || !ok || bcrypt.CompareHashAndPassword([]byte(hash), []byte("fixture-admin-password")) != nil {
		t.Fatal("atomic initialization did not persist winning password")
	}
}

func TestSetupTokenFilePreservesCollisionsAndSymlinks(t *testing.T) {
	for _, kind := range []string{"unrelated", "public", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "public" && runtime.GOOS == "windows" {
				t.Skip("Windows permissions follow the private key directory ACL")
			}
			dir := t.TempDir()
			path := filepath.Join(dir, setupTokenFilename)
			token := deriveSetupToken([]byte("fixture-private-key-32-characters"))
			target := filepath.Join(dir, "operator-file")
			if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unrelated":
				if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.WriteFile(path, []byte(token+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := ensureSetupTokenFile(dir, token); err == nil {
				t.Fatal("collision accepted")
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("existing file replaced or chmodded")
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != "keep" {
				t.Fatal("symlink target modified")
			}
		})
	}
}

func TestPrepareAuthSetupOnlyForUninitializedInstance(t *testing.T) {
	s, store := newAuthMemoryServer(t)
	s.setupToken = ""
	dir := t.TempDir()
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldWriter)
	if err := s.prepareAuthSetup(dir); err != nil {
		t.Fatal(err)
	}
	if s.setupToken != deriveSetupToken(s.jwtKey) || strings.Contains(logs.String(), s.setupToken) || !strings.Contains(logs.String(), filepath.Join(dir, setupTokenFilename)) {
		t.Fatal("startup failed to privately deliver token")
	}
	restarted := &Server{m: s.m, jwtKey: s.jwtKey}
	if err := restarted.prepareAuthSetup(dir); err != nil || restarted.setupToken != s.setupToken {
		t.Fatalf("restart changed ownership proof: %v", err)
	}
	store.values[authPassKey] = "already-initialized"
	untouched := t.TempDir()
	if err := restarted.prepareAuthSetup(untouched); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(untouched)
	if len(entries) != 0 {
		t.Fatal("initialized instance created setup credentials")
	}
	store.err = errors.New("offline")
	if err := restarted.prepareAuthSetup(t.TempDir()); err == nil {
		t.Fatal("DB failure treated as uninitialized")
	}
}

func TestAuthInitRequiresOwnershipBeforePasswordWork(t *testing.T) {
	for _, supplied := range []string{"", "wrong"} {
		t.Run("token="+supplied, func(t *testing.T) {
			s, store := newAuthMemoryServer(t)
			w := authCredentialRequest(s, "init", `{"password":"`+strings.Repeat("a", 73)+`","setup_token":"`+supplied+`"}`, "", nil)
			if w.Code != 403 || len(store.values) != 0 || len(w.Result().Cookies()) != 0 {
				t.Fatalf("ownership gate: %d %s", w.Code, w.Body.String())
			}
		})
	}
	s, store := newAuthMemoryServer(t)
	s.setupToken = ""
	if w := authCredentialRequest(s, "init", `{"password":"test-password","setup_token":""}`, "", nil); w.Code != 403 || len(store.values) != 0 {
		t.Fatal("zero bootstrap token accepted")
	}
	s.setupToken = deriveSetupToken(s.jwtKey)
	store.err = errors.New("offline")
	if w := authCredentialRequest(s, "init", `{"password":"test-password"}`, "", nil); w.Code != 503 {
		t.Fatal("token gate masked data source failure")
	}
	store.err = nil
	store.values[authPassKey] = "initialized"
	if w := authCredentialRequest(s, "init", `{"password":"test-password"}`, "", nil); w.Code != 403 || !strings.Contains(w.Body.String(), "密码已设置") {
		t.Fatal("existing hash did not reject replay before ownership gate")
	}
}

func TestAuthStatusNeverExposesSetupProof(t *testing.T) {
	s, store := newAuthMemoryServer(t)
	for _, initialized := range []bool{false, true} {
		if initialized {
			store.values[authPassKey] = "hash"
		}
		w := httptest.NewRecorder()
		s.authStatus(w, httptest.NewRequest("GET", "/api/auth/status", nil))
		var payload map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || payload["initialized"] != initialized || payload["setup_token_required"] != !initialized || len(payload) != 2 || strings.Contains(w.Body.String(), s.setupToken) {
			t.Fatalf("public status leaked credentials: %s", w.Body.String())
		}
	}
}

func TestAuthInitPasswordBoundariesAndCSRF(t *testing.T) {
	for _, tc := range []struct {
		name, password string
		want           int
	}{
		{"short", "1234567", 400}, {"eight", "12345678", 200},
		{"bcrypt limit", strings.Repeat("a", 72), 200}, {"over limit", strings.Repeat("a", 73), 400},
		{"unicode characters", "密码密码密码密码", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store := newAuthMemoryServer(t)
			w := authCredentialRequest(s, "init", `{"password":"`+tc.password+`","setup_token":"`+s.setupToken+`"}`, "", nil)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d %s", w.Code, tc.want, w.Body.String())
			}
			if tc.want != 200 && len(store.values) != 0 {
				t.Fatal("invalid password mutated credentials")
			}
		})
	}
	s, store := newAuthMemoryServer(t)
	r := httptest.NewRequest("POST", "https://example.test/api/auth/init", strings.NewReader(`{"password":"test-password","setup_token":"`+s.setupToken+`"}`))
	r.Header.Set("Origin", "https://attacker.test")
	w := httptest.NewRecorder()
	s.requireAuth(http.HandlerFunc(s.authInit)).ServeHTTP(w, r)
	if w.Code != 403 || len(store.values) != 0 {
		t.Fatal("bootstrap proof bypassed browser CSRF guard")
	}
	w = httptest.NewRecorder()
	(&Server{}).authInit(w, httptest.NewRequest("POST", "/api/auth/init", strings.NewReader(`{"password":"test-password"}`)))
	if w.Code != 503 {
		t.Fatalf("zero server status=%d want 503", w.Code)
	}
}

func authCredentialRequest(s *Server, action, body, bearer string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "https://example.test/api/auth/"+action, strings.NewReader(body))
	r.Header.Set("Origin", "https://example.test")
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch action {
		case "init":
			s.authInit(w, r)
		case "login":
			s.authLogin(w, r)
		case "change-password":
			s.authChangePassword(w, r)
		}
	})).ServeHTTP(w, r)
	return w
}

func TestPasswordChangeRevokesBearerAndCookieAndRefreshesCurrentBrowser(t *testing.T) {
	s, store := newAuthMemoryServer(t)
	w := authCredentialRequest(s, "init", `{"password":"original-password","setup_token":"`+s.setupToken+`"}`, "", nil)
	if w.Code != 200 {
		t.Fatalf("init: %d %s", w.Code, w.Body.String())
	}
	oldCookie := w.Result().Cookies()[0]
	w = authCredentialRequest(s, "change-password", `{"old_password":"original-password","new_password":"replacement-password"}`, "", oldCookie)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatalf("change: %d %s", w.Code, w.Body.String())
	}
	newCookie := w.Result().Cookies()[0]
	var changed struct {
		Token string `json:"token"`
		OK    bool   `json:"ok"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &changed); err != nil || !changed.OK || changed.Token != newCookie.Value {
		t.Fatal("refreshed CLI/browser session mismatch")
	}
	check := func(token string, cookie bool, want int) {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/private", nil)
		if cookie {
			r.AddCookie(&http.Cookie{Name: "artex_token", Value: token})
		} else {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("cookie=%v status=%d want=%d", cookie, w.Code, want)
		}
	}
	for _, cookie := range []bool{false, true} {
		check(oldCookie.Value, cookie, 401)
		check(newCookie.Value, cookie, 204)
	}
	// An external credential reset is observed immediately, without TTL/cache.
	store.values[authPassKey] = "external-reset-hash"
	check(newCookie.Value, false, 401)
	check(newCookie.Value, true, 401)
	store.err = errors.New("offline")
	check(newCookie.Value, false, 503)
	check(newCookie.Value, true, 503)
	w = authCredentialRequest(s, "change-password", `{"old_password":"replacement-password","new_password":"another-password"}`, newCookie.Value, nil)
	if w.Code != 503 {
		t.Fatalf("change DB failure=%d want 503", w.Code)
	}
}

func TestPasswordChangeConcurrentReplacementIsNotOverwritten(t *testing.T) {
	s, store := newAuthMemoryServer(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("original-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	store.values[authPassKey] = string(hash)
	token, err := s.signAdminJWT(string(hash))
	if err != nil {
		t.Fatal(err)
	}
	store.beforeCAS = func() { store.values[authPassKey] = "concurrent-replacement" }
	w := authCredentialRequest(s, "change-password", `{"old_password":"original-password","new_password":"replacement-password"}`, token, nil)
	if w.Code != 409 || store.values[authPassKey] != "concurrent-replacement" || len(w.Result().Cookies()) != 0 {
		t.Fatalf("lost concurrent change: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminSessionsRequireCurrentCredentialDomainAndClaims(t *testing.T) {
	s, store := newAuthMemoryServer(t)
	store.values[authPassKey] = "stored-hash"
	key := adminSigningKey(s.jwtKey, "stored-hash")
	for _, tc := range []struct {
		name    string
		key     []byte
		method  jwt.SigningMethod
		subject string
		expiry  *jwt.NumericDate
		want    bool
	}{
		{"admin", key, jwt.SigningMethodHS256, "ARTEX", jwt.NewNumericDate(time.Now().Add(time.Hour)), true},
		{"legacy raw", s.jwtKey, jwt.SigningMethodHS256, "ARTEX", jwt.NewNumericDate(time.Now().Add(time.Hour)), false},
		{"basic auth", s.basicAuthSigningKey(), jwt.SigningMethodHS256, "ARTEX", jwt.NewNumericDate(time.Now().Add(time.Hour)), false},
		{"old password", adminSigningKey(s.jwtKey, "old-hash"), jwt.SigningMethodHS256, "ARTEX", jwt.NewNumericDate(time.Now().Add(time.Hour)), false},
		{"no expiry", key, jwt.SigningMethodHS256, "ARTEX", nil, false},
		{"wrong subject", key, jwt.SigningMethodHS256, "other", jwt.NewNumericDate(time.Now().Add(time.Hour)), false},
		{"wrong algorithm", key, jwt.SigningMethodHS384, "ARTEX", jwt.NewNumericDate(time.Now().Add(time.Hour)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, err := jwt.NewWithClaims(tc.method, jwt.RegisteredClaims{Subject: tc.subject, ExpiresAt: tc.expiry}).SignedString(tc.key)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.verifyAdminJWT(token, "stored-hash"); got != tc.want {
				t.Fatalf("accepted=%v want=%v", got, tc.want)
			}
		})
	}
	valid, _ := s.signAdminJWT("stored-hash")
	delete(store.values, authPassKey)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/auth/session", nil)
	r.Header.Set("Authorization", "Bearer "+valid)
	s.authSession(w, r)
	if w.Code != 401 {
		t.Fatalf("missing credential status=%d", w.Code)
	}
	if (&Server{}).verifyAdminJWT(valid, "stored-hash") {
		t.Fatal("unconfigured server authenticated JWT")
	}
}
