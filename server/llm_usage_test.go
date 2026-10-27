package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

func TestLLMUsageAPIValidation(t *testing.T) {
	s := &Server{}
	for _, q := range []string{"", "task=0", "task=042", "task=%2042", "task=9223372036854775808", "task=42&task=43", "task=42&page=-1", "task=42&page=bad", "task=42&page=", "task=42&size=0", "task=42&size=201", "task=42&page=9223372036854775807&size=200", "task=42&session=a&session=b"} {
		w := httptest.NewRecorder()
		s.pgListLLMUsage(w, httptest.NewRequest("GET", "/api/llm/usage?"+q, nil))
		if w.Code != 400 {
			t.Fatalf("%s returned %d: %s", q, w.Code, w.Body.String())
		}
	}
}

func TestLLMUsageHandlerAuthentication(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	schema := fmt.Sprintf("usage_auth_%d", time.Now().UnixNano())
	if _, err := conn.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	if _, err := conn.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	d := &db.DB{DB: conn}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertLLMUsage(&db.LLMUsage{TaskID: "42", SessionID: "auth-session", InputTokens: 7, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: d}, jwtKey: []byte("usage-real-db-auth-signing-key-32")}
	token, err := signAdminJWTForTest(t, s)
	if err != nil {
		t.Fatal(err)
	}
	hash, _, err := d.GetSetting(authPassKey)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "ARTEX", ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour))}).SignedString(adminSigningKey(s.jwtKey, hash))
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	request := func(token string, basic bool, user, password string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/llm/usage?task=42", nil)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "artex_token", Value: token})
		}
		if basic {
			r.SetBasicAuth(user, password)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	assertResponse := func(w *httptest.ResponseRecorder, want int) {
		t.Helper()
		if w.Code != want {
			t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
		}
		if want == 200 {
			var result struct {
				Records []db.LLMUsageRecord `json:"records"`
				Total   int                 `json:"total"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Total != 1 || len(result.Records) != 1 || result.Records[0].TaskID != "42" || result.Records[0].SessionID != "auth-session" {
				t.Fatalf("metadata=%s err=%v", w.Body.String(), err)
			}
		} else if strings.Contains(w.Body.String(), "auth-session") {
			t.Fatal("unauthorized response leaked usage")
		}
	}
	assertResponse(request("", false, "", ""), 401)
	assertResponse(request(token, false, "", ""), 200)
	assertResponse(request(expired, false, "", ""), 401)
	assertResponse(request("wrong-credential", false, "", ""), 401)
	gateHash, err := bcrypt.GenerateFromPassword([]byte("entry-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	gateJSON, err := json.Marshal(basicAuthConfig{Enabled: true, Username: "entry", PasswordHash: string(gateHash), Revision: "usage-auth-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting(basicAuthKey, string(gateJSON)); err != nil {
		t.Fatal(err)
	}
	if err := s.basicAuth.load(basicAuthDBStore{d}); err != nil {
		t.Fatal(err)
	}
	assertResponse(request(token, false, "", ""), 401)
	assertResponse(request(token, true, "entry", "wrong-password"), 401)
	assertResponse(request("", true, "entry", "entry-password"), 401)
	assertResponse(request(token, true, "entry", "entry-password"), 200)
	assertResponse(request(expired, true, "entry", "entry-password"), 401)
	if err := d.SetSetting(basicAuthKey, `{"enabled":false}`); err != nil {
		t.Fatal(err)
	}
	if err := s.basicAuth.load(basicAuthDBStore{d}); err != nil {
		t.Fatal(err)
	}
	assertResponse(request("", true, "entry", "entry-password"), 401)
	assertResponse(request(token, false, "", ""), 200)
}

// 显式数据库输入仅验证诊断接口，不代表实际模型调用。
func TestLLMUsageAPIRealDatabase(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	task := strconv.FormatInt(time.Now().UnixNano(), 10)
	defer d.Exec(`DELETE FROM llm_usage WHERE task_id=$1`, task)
	for _, session := range []string{"", "conv-one", "conv-two"} {
		if err := d.InsertLLMUsage(&db.LLMUsage{TaskID: task, SessionID: session, InputTokens: 7, Status: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{m: &Manager{pg: d}}
	for _, tc := range []struct {
		q            string
		total, count int
	}{{"", 3, 3}, {"&session=conv-one", 1, 1}, {"&session=", 1, 1}, {"&page=9&size=2", 3, 0}, {"&size=2", 3, 2}} {
		w := httptest.NewRecorder()
		s.pgListLLMUsage(w, httptest.NewRequest("GET", "/api/llm/usage?task="+task+tc.q, nil))
		var response struct {
			Records []db.LLMUsageRecord `json:"records"`
			Total   int                 `json:"total"`
			Page    int                 `json:"page"`
			Size    int                 `json:"size"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || response.Total != tc.total || len(response.Records) != tc.count || response.Records == nil {
			t.Fatalf("%s: %d %s", tc.q, w.Code, w.Body.String())
		}
		for _, key := range []string{"request_body", "response_body", "raw_request", "api_key"} {
			if strings.Contains(w.Body.String(), key) {
				t.Fatalf("body metadata leaked %s", key)
			}
		}
	}
	ctxRequest := httptest.NewRequest("GET", "/api/llm/usage?task="+task, nil)
	ctx, cancel := context.WithCancel(ctxRequest.Context())
	cancel()
	w := httptest.NewRecorder()
	s.pgListLLMUsage(w, ctxRequest.WithContext(ctx))
	if w.Code != 500 {
		t.Fatalf("canceled request=%d", w.Code)
	}
}
