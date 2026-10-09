package server

import (
	"encoding/json"
	"github.com/Autumn-27/artex/db"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestBrowserSessionCookieAndLogout(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	token, err := signJWT(key)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{jwtKey: key}
	for _, scheme := range []string{"http", "https"} {
		r := httptest.NewRequest("GET", scheme+"://example.test/api/auth/session", nil)
		w := httptest.NewRecorder()
		setAuthCookie(w, r, token, int(jwtTTL.Seconds()))
		cookie := w.Result().Cookies()[0]
		if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.MaxAge != 604800 || cookie.Secure != (scheme == "https") {
			t.Fatalf("cookie=%+v", cookie)
		}
		r.AddCookie(cookie)
		recovered := httptest.NewRecorder()
		s.authSession(recovered, r)
		if recovered.Code != 200 || recovered.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("session %d %s", recovered.Code, recovered.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.authLogout(w, httptest.NewRequest("POST", "http://example.test/api/auth/logout", nil))
	cookie := w.Result().Cookies()[0]
	if cookie.Value != "" || cookie.MaxAge != -1 || !cookie.HttpOnly {
		t.Fatalf("logout cookie=%+v", cookie)
	}
	invalid := httptest.NewRequest("GET", "http://example.test/api/auth/session", nil)
	invalid.AddCookie(&http.Cookie{Name: "artex_token", Value: "expired"})
	w = httptest.NewRecorder()
	s.authSession(w, invalid)
	if w.Code != 401 || len(w.Result().Cookies()) != 0 {
		t.Fatalf("invalid session clears cookie or status=%d", w.Code)
	}
}

func TestBearerAndCookieAuthenticationWithCSRF(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	token, _ := signJWT(key)
	s := &Server{jwtKey: key}
	h := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		name, origin, bearer string
		cookie               bool
		want                 int
	}{
		{"cookie same origin", "http://localhost:8787", "", true, 204},
		{"cookie dev frontend", "http://localhost:3000", "", true, 204},
		{"cookie foreign origin", "https://attacker.test", "", true, 403},
		{"cookie foreign port", "http://localhost:9999", "", true, 403},
		{"Bearer API", "https://attacker.test", token, false, 204},
		{"explicit invalid Bearer overrides valid cookie", "http://localhost:8787", "invalid", true, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://localhost:8787/api/settings", nil)
			r.Header.Set("Origin", tc.origin)
			if tc.cookie {
				r.AddCookie(&http.Cookie{Name: "artex_token", Value: token})
			}
			if tc.bearer != "" {
				r.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
			if len(w.Result().Cookies()) != 0 {
				t.Fatal("generic response must not clear cookies")
			}
		})
	}
	r := httptest.NewRequest("GET", "http://localhost:8787/api/settings?token="+token, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("URL tokens must not authenticate")
	}
}

func TestCredentialedDevCORS(t *testing.T) {
	h := cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, origin := range []string{"http://localhost:3000", "http://attacker.test:3000"} {
		r := httptest.NewRequest("GET", "http://localhost:8787/api/logs/stream", nil)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if origin == "http://localhost:3000" {
			if w.Header().Get("Access-Control-Allow-Origin") != origin || w.Header().Get("Access-Control-Allow-Credentials") != "true" {
				t.Fatal("dev credentials not allowed")
			}
		} else if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("foreign CORS origin allowed")
		}
	}
}

func TestBrowserOriginProxyAndLAN(t *testing.T) {
	for _, tc := range []struct {
		name, host, origin, peer, forwarded string
		want                                bool
	}{
		{"LAN dev", "192.168.1.2:8787", "http://192.168.1.2:3000", "192.168.1.3:1234", "", true},
		{"different LAN host", "192.168.1.2:8787", "http://192.168.1.3:3000", "192.168.1.3:1234", "", false},
		{"public hostname port transition", "example.test:8787", "http://example.test:3000", "192.168.1.3:1234", "", false},
		{"Next rewrite", "localhost:8787", "http://192.168.1.2:3000", "127.0.0.1:1234", "192.168.1.2:3000", true},
		{"spoofed forwarded host", "localhost:8787", "http://attacker.test", "192.168.1.3:1234", "attacker.test", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://"+tc.host+"/api/settings", nil)
			r.RemoteAddr = tc.peer
			r.Header.Set("X-Forwarded-Host", tc.forwarded)
			if got := allowedBrowserOrigin(r, tc.origin); got != tc.want {
				t.Fatalf("allowed=%v want=%v", got, tc.want)
			}
		})
	}
	r := httptest.NewRequest("POST", "http://example.test/api/auth/login", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	setAuthCookie(w, r, "token", 604800)
	if !w.Result().Cookies()[0].Secure || !allowedBrowserOrigin(r, "https://example.test") {
		t.Fatal("TLS terminating proxy unsupported")
	}
}

// Opt in with a dedicated, empty fixture DB; never read the application's DSN.
func TestAuthLoginAndSetupIssueBrowserCookie(t *testing.T) {
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
	defer func() {
		if _, err := pg.Exec("DELETE FROM settings WHERE key=$1", authPassKey); err != nil {
			t.Error(err)
		}
	}()
	key := []byte("01234567890123456789012345678901")
	s := &Server{m: &Manager{pg: pg}, jwtKey: key}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/init", s.authInit)
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.HandleFunc("GET /api/auth/session", s.authSession)
	mux.HandleFunc("POST /api/auth/logout", s.authLogout)
	mux.HandleFunc("GET /api/private", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	h := s.requireAuth(mux)
	for _, path := range []string{"init", "login"} {
		r := httptest.NewRequest("POST", "https://example.test/api/auth/"+path, strings.NewReader(`{"username":"ARTEX","password":"test-password"}`))
		r.Header.Set("Origin", "https://example.test")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
		var result struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !verifyJWT(result.Token, key) {
			t.Fatalf("CLI JSON token invalid: %v", err)
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Value != result.Token || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].MaxAge != 604800 {
			t.Fatalf("%s cookie=%+v", path, cookies)
		}
		for _, mode := range []string{"cookie", "Bearer"} {
			r = httptest.NewRequest("GET", "https://example.test/api/private", nil)
			if mode == "cookie" {
				r.AddCookie(cookies[0])
			} else {
				r.Header.Set("Authorization", "Bearer "+result.Token)
			}
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 204 {
				t.Fatalf("%s authentication failed: %d", mode, w.Code)
			}
		}
	}
}
