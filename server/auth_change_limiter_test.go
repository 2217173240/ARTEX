package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

func TestAuthChangePasswordRateLimitPG(t *testing.T) {
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
	hash, err := bcrypt.GenerateFromPassword([]byte("original-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := pg.SetSetting(authPassKey, string(hash)); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg}, jwtKey: []byte("fixture-private-key-32-characters")}
	now := time.Unix(1700000000, 0)
	s.authLimiter.now = func() time.Time { return now }
	s.passwordChangeLimiter.now = func() time.Time { return now }
	token, err := s.signAdminJWT(string(hash))
	if err != nil {
		t.Fatal(err)
	}
	wrong := `{"old_password":"wrong-password","new_password":"replacement-password"}`
	correct := `{"old_password":"original-password","new_password":"replacement-password"}`
	for range authAttemptLimit + 1 {
		w := authCredentialRequest(s, "change-password", wrong, "", nil)
		if w.Code != http.StatusUnauthorized || w.Header().Get("Retry-After") != "" {
			t.Fatalf("unauthenticated change: %d %s", w.Code, w.Body.String())
		}
	}
	for i := range authAttemptLimit {
		w := authCredentialRequest(s, "change-password", wrong, token, nil)
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "当前密码错误") || w.Header().Get("Retry-After") != "" {
			t.Fatalf("old-password attempt %d: %d %s", i+1, w.Code, w.Body.String())
		}
	}
	// The exhausted quota applies before JSON decoding and password hashing.
	for _, body := range []string{wrong, correct, "{"} {
		w := authCredentialRequest(s, "change-password", body, token, nil)
		if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" || len(w.Result().Cookies()) != 0 {
			t.Fatalf("exhausted change: %d %s", w.Code, w.Body.String())
		}
	}
	w := authCredentialRequest(s, "login", `{"username":"ARTEX","password":"original-password"}`, "", nil)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("shared login quota: %d %s", w.Code, w.Body.String())
	}
	if stored, _, err := pg.GetSetting(authPassKey); err != nil || stored != string(hash) {
		t.Fatalf("rejected changes modified credential: %v", err)
	}
	requestPeer := func(action, peer, bearer, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://example.test/api/auth/"+action, strings.NewReader(body))
		r.RemoteAddr = peer
		r.Header.Set("Origin", "https://example.test")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler := s.authChangePassword
		if action == "login" {
			handler = s.authLogin
		}
		s.requireAuth(http.HandlerFunc(handler)).ServeHTTP(w, r)
		return w
	}
	// An exhausted session remains blocked at a fresh peer. A different signed
	// session and normal login at other peers retain their own quota.
	w = requestPeer("change-password", "203.0.113.10:1000", token, wrong)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" {
		t.Fatalf("session bypass through new peer: %d %s", w.Code, w.Body.String())
	}
	otherToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject: "ARTEX", ID: "independent-session", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}).SignedString(adminSigningKey(s.jwtKey, string(hash)))
	if err != nil {
		t.Fatal(err)
	}
	w = requestPeer("change-password", "203.0.113.11:1000", otherToken, wrong)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "当前密码错误") {
		t.Fatalf("independent session blocked: %d %s", w.Code, w.Body.String())
	}
	w = requestPeer("login", "203.0.113.10:1001", "", `{"username":"ARTEX","password":"original-password"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("independent login blocked: %d %s", w.Code, w.Body.String())
	}
	now = now.Add(authAttemptPeriod)
	w = authCredentialRequest(s, "change-password", correct, token, nil)
	if w.Code != http.StatusOK || w.Header().Get("Retry-After") != "" || len(w.Result().Cookies()) != 1 {
		t.Fatalf("change after expiry: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	stored, exists, err := pg.GetSetting(authPassKey)
	if err != nil || !exists || bcrypt.CompareHashAndPassword([]byte(stored), []byte("replacement-password")) != nil || !s.verifyAdminJWT(result.Token, stored) || s.verifyAdminJWT(token, stored) {
		t.Fatalf("password/session transition failed: exists=%v err=%v", exists, err)
	}
}
