package server

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

func TestInterceptGetJudgeConfigReturnsStorageError(t *testing.T) {
	conn, err := sql.Open("pgx", "postgres://invalid@127.0.0.1:1/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{interceptor: intercept.New(&db.DB{DB: conn})}}
	w := httptest.NewRecorder()
	s.interceptGetJudgeConfig(w, httptest.NewRequest(http.MethodGet, "/api/intercept/judge", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("storage failure returned a successful disabled config: status=%d body=%s", w.Code, w.Body.String())
	}
}
