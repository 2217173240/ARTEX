package server

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/sidequestion"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Every fixture owns its database; no developer credential or task is modified.
func streamAuthFixture(t *testing.T) *Server {
	t.Helper()
	raw := os.Getenv("ARTEX_SSE_TEST_ADMIN_DSN")
	if raw == "" {
		t.Skip("ARTEX_SSE_TEST_ADMIN_DSN required for disposable SSE integration database")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("SSE admin DSN must be a PostgreSQL URL")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("artex_sse_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	u.Path, u.RawPath = "/"+name, ""
	t.Setenv("ARTEX_PG_DSN", u.String())
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return &Server{m: m, ctx: context.Background(), engine: &Engine{bc: NewBroadcaster()}, jwtKey: []byte("sse-test-private-signing-key-32bytes")}
}

func TestSSEAuthorizationUnavailableAtEntry(t *testing.T) {
	for _, s := range []*Server{{}, newAuthControlsServer(t)} {
		for name, handler := range map[string]http.HandlerFunc{"logs": s.streamLogs, "activity": s.streamActivity, "update": s.updateStream, "side": s.sideEvents} {
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest(http.MethodGet, "/", nil))
			if w.Code != http.StatusServiceUnavailable || w.Header().Get("Content-Type") == "text/event-stream" {
				t.Fatalf("%s: expected unavailable before SSE starts, got %d", name, w.Code)
			}
		}
	}
}

func TestSSEAuthorizationLifecycle(t *testing.T) {
	s := streamAuthFixture(t)
	task, err := s.m.CreateTask("SSE authorization", "subscription isolation", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.m.pg.CreateConversation("mainagent", "SSE authorization", nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := sidequestion.Snapshot{Parent: sidequestion.Parent{ConversationID: c.ID}, RunID: 1, Version: 1, CapturedAt: time.Now().UTC()}
	if err := s.m.pg.SaveSideSnapshot(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	exchange, _, err := s.m.pg.StartSideRequest(t.Context(), snapshot, "sse-client", "running question")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /logs", s.streamLogs)
	mux.HandleFunc("GET /activity", s.streamActivity)
	mux.HandleFunc("GET /update", s.updateStream)
	mux.HandleFunc("GET /side/{requestID}", s.sideEvents)
	mux.HandleFunc("POST /change-password", s.authChangePassword)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	paths := []string{"/logs", "/activity?task=" + task.ID, "/update", "/side/" + exchange.ID}
	for _, mode := range []string{"password change", "external reset", "credential removed", "credential unreadable", "credential read blocked", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			hash := "sse-original"
			if mode == "password change" {
				encoded, err := bcrypt.GenerateFromPassword([]byte("sse-original-password"), bcrypt.DefaultCost)
				if err != nil {
					t.Fatal(err)
				}
				hash = string(encoded)
			}
			if err := s.m.pg.SetSetting(authPassKey, hash); err != nil {
				t.Fatal(err)
			}
			token, err := s.signAdminJWT(hash)
			if mode == "expiry" {
				token, err = jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "ARTEX", ExpiresAt: jwt.NewNumericDate(time.Now().Add(2 * time.Second))}).SignedString(adminSigningKey(s.jwtKey, "sse-original"))
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 9*time.Second)
			defer cancel()
			done := make(chan error, len(paths))
			for _, path := range paths {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+path, nil)
				req.AddCookie(&http.Cookie{Name: "artex_token", Value: token})
				response, err := httpServer.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
					response.Body.Close()
					t.Fatalf("%s: %s", path, response.Status)
				}
				go func() {
					defer response.Body.Close()
					body, err := io.ReadAll(response.Body)
					if err == nil && (strings.Contains(string(body), "未授权") || strings.Contains(string(body), errDataSourceUnavailable)) {
						err = fmt.Errorf("JSON error appended to SSE: %s", body)
					}
					done <- err
				}()
			}
			switch mode {
			case "password change":
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/change-password", strings.NewReader(`{"old_password":"sse-original-password","new_password":"sse-replacement-password"}`))
				req.Header.Set("Authorization", "Bearer "+token)
				response, requestErr := httpServer.Client().Do(req)
				if requestErr != nil {
					t.Fatal(requestErr)
				}
				response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("change password: %s", response.Status)
				}
			case "external reset":
				_, err = s.m.pg.Exec(`UPDATE settings SET value='sse-replacement' WHERE key=$1`, authPassKey)
			case "credential removed":
				_, err = s.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, authPassKey)
			case "credential unreadable":
				_, err = s.m.pg.Exec(`ALTER TABLE settings RENAME TO sse_settings_unavailable`)
				defer func() {
					if _, err := s.m.pg.Exec(`ALTER TABLE sse_settings_unavailable RENAME TO settings`); err != nil {
						t.Error(err)
					}
				}()
			case "credential read blocked":
				var tx *sql.Tx
				tx, err = s.m.pg.Begin()
				if err == nil {
					defer tx.Rollback()
					_, err = tx.Exec(`LOCK TABLE settings IN ACCESS EXCLUSIVE MODE`)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "credential unreadable" {
				for _, path := range paths {
					req, _ := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+path, nil)
					req.Header.Set("Authorization", "Bearer "+token)
					response, err := httpServer.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					response.Body.Close()
					if response.StatusCode != http.StatusServiceUnavailable {
						t.Fatalf("unreadable credential accepted fresh stream %s: %s", path, response.Status)
					}
				}
			}
			// Continuous real hub output exercises the same fixed deadline as idle
			// streams: active progress must not keep a revoked session alive.
			tick := time.NewTicker(25 * time.Millisecond)
			defer tick.Stop()
			remaining := len(paths)
			for remaining > 0 {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
					remaining--
				case <-tick.C:
					updHub.mu.Lock()
					updHub.fanout(updateProgress{Message: "live progress"})
					updHub.mu.Unlock()
				case <-ctx.Done():
					t.Fatal("SSE subscription did not close within authorization deadline")
				}
			}
			updHub.mu.Lock()
			left := len(updHub.subs)
			updHub.mu.Unlock()
			if left != 0 {
				t.Fatal("update subscription retained after revocation")
			}
			s.engine.Broadcaster().mu.Lock()
			left = len(s.engine.Broadcaster().subs)
			s.engine.Broadcaster().mu.Unlock()
			if left != 0 {
				t.Fatal("activity subscription retained after revocation")
			}
			logSink.mu.Lock()
			left = len(logSink.subs)
			logSink.mu.Unlock()
			if left != 0 {
				t.Fatal("log subscription retained after revocation")
			}
			current, err := s.m.pg.SideRequest(t.Context(), exchange.ID)
			if err != nil || current == nil || !current.Running() {
				t.Fatalf("subscription cancellation changed running side question: %v", err)
			}
		})
	}
	if err := s.m.pg.SetSetting(authPassKey, "sse-replacement"); err != nil {
		t.Fatal(err)
	}
	newToken, _ := s.signAdminJWT("sse-replacement")
	oldToken, _ := s.signAdminJWT("sse-original")
	for _, path := range paths {
		for _, tc := range []struct {
			token, query string
			want         int
		}{
			{oldToken, "", http.StatusUnauthorized}, {"", newToken, http.StatusUnauthorized}, {newToken, "", http.StatusOK},
		} {
			ctx, cancel := context.WithCancel(t.Context())
			target := httpServer.URL + path
			if tc.query != "" {
				sep := "?"
				if strings.Contains(path, "?") {
					sep = "&"
				}
				target += sep + "token=" + tc.query
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			response, err := httpServer.Client().Do(req)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			response.Body.Close()
			cancel()
			if response.StatusCode != tc.want {
				t.Fatalf("%s: got %d want %d", path, response.StatusCode, tc.want)
			}
		}
	}
}
