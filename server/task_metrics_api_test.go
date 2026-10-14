package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestTaskDetailMetricsMatchList(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("requires isolated ARTEX_PG_DSN fixture")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	task, err := m.CreateTask("metrics fixture", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM findings WHERE task_id=$1`, tid); _ = m.pg.DeleteTask(tid) })
	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)
	token, err := signAdminJWTForTest(t, s)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	request := func(path string, authorized bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if authorized {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	read := func() (TaskDTO, TaskDTO) {
		w := request("/api/tasks", true)
		if w.Code != 200 {
			t.Fatalf("list %d: %s", w.Code, w.Body.String())
		}
		var list struct {
			Tasks []TaskDTO `json:"tasks"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		var listed TaskDTO
		for _, item := range list.Tasks {
			if item.ID == task.ID {
				listed = item
			}
		}
		w = request("/api/tasks/"+task.ID, true)
		if w.Code != 200 {
			t.Fatalf("detail %d: %s", w.Code, w.Body.String())
		}
		var detail TaskDTO
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(listed, detail) {
			t.Fatalf("list/detail mismatch\nlist: %+v\ndetail: %+v", listed, detail)
		}
		return listed, detail
	}
	listed, _ := read()
	if listed.DistinctFindings != nil {
		t.Fatal("empty task should omit distinct_findings")
	}
	for _, query := range []string{
		`INSERT INTO activity(exploration_id,kind,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,created_at) VALUES ($1,'result',11,7,3,2,'2025-01-01 UTC'),($1,'tool_use',999,999,999,999,'2025-01-02 UTC')`,
		`INSERT INTO exploration_nodes(exploration_id,kind,state) VALUES ($1,'goal','met'),($1,'goal','open'),($1,'intent','running'),($1,'intent','open')`,
	} {
		if _, err = m.pg.Exec(query, task.ExpID); err != nil {
			t.Fatal(err)
		}
	}
	ids := []int64{}
	for _, sev := range []string{"high", "medium", "low"} {
		id, err := m.pg.AddFinding(tid, 0, "fixture", "fixture", sev, "fixture", "synthetic evidence", "fixture", nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err = m.pg.MergeFindingCase(context.Background(), tid, ids[:2], "fixture case", "synthetic cause", "fixture"); err != nil {
		t.Fatal(err)
	}
	listed, _ = read()
	if listed.Tokens.InputTokens != 11 || listed.GoalsTotal != 2 || listed.GoalsMet != 1 || listed.InFlight != 1 || listed.Findings.High != 1 || listed.DistinctFindings == nil || listed.DistinctFindings.Total != 2 {
		t.Fatalf("wrong metrics: %+v", listed)
	}
	persisted := listed.LastActivity
	s.engine.lastAct.Store(task.ID, persisted+60)
	listed, _ = read()
	if listed.LastActivity <= persisted {
		t.Fatal("live heartbeat did not supersede persisted activity")
	}
	task.lifecycleMu.Lock()
	task.Status = "done"
	task.lifecycleMu.Unlock()
	listed, _ = read()
	if listed.Status != "done" {
		t.Fatalf("terminal status: %s", listed.Status)
	}
	child, err := m.pg.CreateTaskWithOptions("dependent fixture", "goal", db.TaskCreateOptions{SourceTaskIDs: []int64{tid}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteTask(child.ID) })
	listed, _ = read()
	if listed.ArchiveBlockedBy != strconv.FormatInt(child.ID, 10) {
		t.Fatalf("missing archive blocker: %+v", listed)
	}
	if err := m.pg.DeleteTask(child.ID); err != nil {
		t.Fatal(err)
	}
	listed, _ = read()
	if listed.ArchiveBlockedBy != "" {
		t.Fatal("deleted child blocks archive")
	}
	if w := request("/api/tasks/999999999", true); w.Code != 404 {
		t.Fatalf("missing task: %d", w.Code)
	}
	if w := request("/api/tasks/"+task.ID, false); w.Code != 401 {
		t.Fatalf("unauthorized: %d", w.Code)
	}
}
