package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestTaskOutcomeValidationAndPartialAvailability(t *testing.T) {
	for _, id := range []string{"", "0", "-1", "abc", "1.2", "9223372036854775808"} {
		r := httptest.NewRequest(http.MethodGet, "/api/tasks/invalid/outcome", nil)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		(&Server{}).taskOutcome(w, r)
		assertErrorResponse(t, w, http.StatusBadRequest, "invalid task id")
	}
	for _, s := range []*Server{{}, {m: &Manager{}}} {
		r := httptest.NewRequest(http.MethodGet, "/api/tasks/1/outcome", nil)
		r.SetPathValue("id", "1")
		w := httptest.NewRecorder()
		s.taskOutcome(w, r)
		assertErrorResponse(t, w, http.StatusServiceUnavailable, "database unavailable")
	}
	w := httptest.NewRecorder()
	writeArchiveError(w, db.ErrTaskArchiveRetestActive)
	assertErrorResponse(t, w, http.StatusConflict, db.ErrTaskArchiveRetestActive.Error())
}

func TestTaskOutcomeAPIAndDatabaseFailure(t *testing.T) {
	if dsn := os.Getenv("ARTEX_PG_DSN"); dsn == "" || dsn == "invalid" {
		t.Skip("requires isolated ARTEX_PG_DSN fixture")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	task, err := m.CreateTask("outcome API fixture", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	t.Cleanup(func() { _ = m.pg.DeleteTask(id) })
	if _, err := m.pg.Exec(`INSERT INTO findings(task_id,vulnclass,severity,status) VALUES($1,'fixture','critical','confirmed')`, id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM findings WHERE task_id=$1`, id) })
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	td := t.TempDir()
	s := New(ctx, m, td, td, td)
	token, err := signAdminJWTForTest(t, s)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	get := func(taskID string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/tasks/"+taskID+"/outcome", nil)
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := get(task.ID, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized outcome status=%d", w.Code)
	}
	w := get(task.ID, true)
	if w.Code != http.StatusOK {
		t.Fatalf("outcome status=%d body=%s", w.Code, w.Body.String())
	}
	var out db.TaskOutcome
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.TaskID != id || out.Findings.Total != 1 || out.Findings.ConfirmedTotal != 1 || out.Findings.Critical != 1 || out.Source != "live" || out.Partial || out.WallSeconds != nil || out.Scope.Cost == "" {
		t.Fatalf("outcome=%+v", out)
	}
	if w := get("9223372036854775807", true); w.Code != http.StatusNotFound {
		t.Fatalf("missing task status=%d", w.Code)
	}
	if w := get("invalid", true); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid task status=%d", w.Code)
	}
	// 使用真实已关闭连接验证数据库故障只返回固定公开错误。
	faultDB, err := sql.Open("pgx", os.Getenv("ARTEX_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	if err := faultDB.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := faultDB.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/outcome", nil)
	r.SetPathValue("id", task.ID)
	w = httptest.NewRecorder()
	(&Server{m: &Manager{pg: &db.DB{DB: faultDB}}}).taskOutcome(w, r)
	assertErrorResponse(t, w, http.StatusInternalServerError, internalErrorMessage)
}
