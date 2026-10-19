package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestTaskEvidenceValidationAndAvailability(t *testing.T) {
	for _, query := range []string{"kind=unknown", "limit=0", "limit=201", "limit=abc", "before=-1", "before=abc", "q=" + strings.Repeat("x", 501)} {
		r := httptest.NewRequest(http.MethodGet, "/api/tasks/1/evidence?"+query, nil)
		r.SetPathValue("id", "1")
		w := httptest.NewRecorder()
		(&Server{}).taskEvidenceList(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("query=%q status=%d", query, w.Code)
		}
	}
	for _, id := range []string{"0", "-1", "invalid", "9223372036854775808"} {
		r := httptest.NewRequest(http.MethodGet, "/api/tasks/invalid/evidence", nil)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		(&Server{}).taskEvidenceList(w, r)
		assertErrorResponse(t, w, http.StatusBadRequest, "invalid task id")
	}
	r := httptest.NewRequest(http.MethodGet, "/api/tasks/1/evidence", nil)
	r.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	(&Server{}).taskEvidenceList(w, r)
	assertErrorResponse(t, w, http.StatusServiceUnavailable, "database unavailable")
}

func TestTaskEvidenceBodySurvivesFindingUnbind(t *testing.T) {
	if dsn := os.Getenv("ARTEX_PG_DSN"); dsn == "" || dsn == "invalid" {
		t.Skip("requires isolated ARTEX_PG_DSN fixture")
	}
	s, finding, request := trafficEvidenceServer(t)
	ctx := t.Context()
	body := bytes.Repeat([]byte("原始证据\n"), 3000)
	seedServerEvidenceFlow(t, s, "observation-body-api", body)
	bound, err := s.evidenceStore().Bind(ctx, finding.FindingID, []db.TrafficRef{{TrafficID: "observation-body-api"}})
	if err != nil {
		t.Fatal(err)
	}
	var taskID int64
	if err := s.m.pg.QueryRow(`SELECT task_id FROM findings WHERE id=$1`, finding.FindingID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	task, err := s.m.pg.GetTask(taskID)
	if err != nil || task == nil {
		t.Fatal("task lookup", err)
	}
	store := s.m.pg.Exploration(task.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"description": "local body fixture"}, 5, nil, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	binding := bound.Bindings[0]
	observation, err := store.RecordObservation(ctx, db.ObservationInput{TaskID: taskID, IntentID: intent, Summary: "保存的正文", SourceKind: "traffic_snapshot", SourceID: binding.SnapshotID, ContextKey: "local-body-fixture", Worker: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.m.pg.EditFindingTraffic(ctx, finding.FindingID, binding.ID, bound.Version, nil, nil, true, nil); err != nil {
		t.Fatal(err)
	}
	base := "/api/tasks/" + strconv.FormatInt(taskID, 10) + "/evidence/" + strconv.FormatInt(observation.ID, 10) + "/body"
	w := request(http.MethodGet, base+"?side=response", "")
	var preview evidencePreview
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.Total != int64(len(body)) || !preview.Truncated || !strings.HasPrefix(string(body), preview.Content) {
		t.Fatalf("body preview=%d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodGet, base+"?side=response&download=1", "")
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), body) || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("body download=%d bytes=%d", w.Code, w.Body.Len())
	}
	for _, query := range []string{"side=invalid", "offset=-1", "length=-1", "offset=9223372036854775807"} {
		if w := request(http.MethodGet, base+"?"+query, ""); w.Code != http.StatusBadRequest {
			t.Fatalf("body range %s=%d", query, w.Code)
		}
	}
	other, err := s.m.CreateTask("foreign body fixture", "local only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	otherID, _ := strconv.ParseInt(other.ID, 10, 64)
	t.Cleanup(func() { _ = s.m.pg.DeleteTask(otherID) })
	foreign := "/api/tasks/" + other.ID + "/evidence/" + strconv.FormatInt(observation.ID, 10) + "/body"
	if w := request(http.MethodGet, foreign, ""); w.Code != http.StatusNotFound {
		t.Fatalf("foreign body scope=%d %s", w.Code, w.Body.String())
	}
}

func TestTaskEvidenceRoutesScopeAndPagination(t *testing.T) {
	if dsn := os.Getenv("ARTEX_PG_DSN"); dsn == "" || dsn == "invalid" {
		t.Skip("requires isolated ARTEX_PG_DSN fixture")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	create := func() (*Task, int64, *db.Node) {
		t.Helper()
		task, err := m.CreateTask("evidence API fixture", "local fixture only", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		tid, _ := strconv.ParseInt(task.ID, 10, 64)
		t.Cleanup(func() {
			_, _ = m.pg.Exec(`DELETE FROM task_archives WHERE task_id=$1`, tid)
			_ = m.pg.DeleteTask(tid)
		})
		intent, err := task.Store.AddNode(db.KindIntent, map[string]any{"description": "local observation fixture"}, 5, "open", "fixture", nil)
		if err != nil {
			t.Fatal(err)
		}
		observation, err := task.Store.RecordObservation(t.Context(), db.ObservationInput{
			TaskID: tid, IntentID: intent, Summary: "记录 fixture α", Detail: "model-recorded fixture", SourceKind: "model_recorded", ContextKey: "local-fixture", Worker: "fixture",
		})
		if err != nil {
			t.Fatal(err)
		}
		return task, tid, observation
	}
	task, taskID, first := create()
	other, _, foreign := create()
	second, err := task.Store.RecordObservation(t.Context(), db.ObservationInput{
		TaskID: taskID, IntentID: payloadIntent(t, first), Summary: "第二条 fixture α", SourceKind: "model_recorded", ContextKey: "local-fixture", Worker: "fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dir := t.TempDir()
	s := New(ctx, m, dir, dir, dir)
	s.archiveWG.Wait()
	token, err := signAdminJWTForTest(t, s)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	get := func(path string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	base := "/api/tasks/" + task.ID + "/evidence"
	if w := get(base, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d", w.Code)
	}
	var page struct {
		Nodes      []TaskNodeDTO `json:"nodes"`
		NextBefore int64         `json:"next_before"`
		Scope      string        `json:"scope"`
	}
	w := get(base+"?kind=observation&limit=1&q="+url.QueryEscape("fixture α"), true)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Nodes) != 1 || page.Nodes[0].ID != strconv.FormatInt(second.ID, 10) || page.NextBefore != second.ID || page.Scope != "task_only" {
		t.Fatalf("first page=%d %s", w.Code, w.Body.String())
	}
	w = get(base+"?kind=observation&limit=1&before="+strconv.FormatInt(page.NextBefore, 10), true)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Nodes) != 1 || page.Nodes[0].ID != strconv.FormatInt(first.ID, 10) {
		t.Fatalf("second page=%d %s", w.Code, w.Body.String())
	}
	var detail struct {
		Node  TaskNodeDTO   `json:"node"`
		Nodes []TaskNodeDTO `json:"nodes"`
	}
	w = get(base+"/"+strconv.FormatInt(first.ID, 10), true)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.Node.Type != db.KindObservation || detail.Node.Payload == "" || len(detail.Nodes) < 2 {
		t.Fatalf("detail=%d %s", w.Code, w.Body.String())
	}
	if w := get(base+"/"+strconv.FormatInt(foreign.ID, 10), true); w.Code != http.StatusNotFound {
		t.Fatalf("foreign task %s node returned %d %s", other.ID, w.Code, w.Body.String())
	}
	if w := get("/api/tasks/9223372036854775807/evidence", true); w.Code != http.StatusNotFound {
		t.Fatalf("missing task=%d", w.Code)
	}
	if err := m.pg.SetPaused(taskID, true); err != nil {
		t.Fatal(err)
	}
	archive, err := m.pg.QueueTaskArchive(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.Exec(`UPDATE task_archives SET state='ready' WHERE id=$1`, archive.ID); err != nil {
		t.Fatal(err)
	}
	if w := get(base, true); w.Code != http.StatusConflict {
		t.Fatalf("cold archive=%d %s", w.Code, w.Body.String())
	}
}

func payloadIntent(t *testing.T, node *db.Node) int64 {
	t.Helper()
	var payload db.ObservationPayload
	if err := json.Unmarshal(node.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.IntentID
}

func TestEvidenceToolUpgradePreservesRuntimeCustomization(t *testing.T) {
	if dsn := os.Getenv("ARTEX_PG_DSN"); dsn == "" || dsn == "invalid" {
		t.Skip("requires isolated ARTEX_PG_DSN fixture")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	before := agent.ToolResolve
	t.Cleanup(func() { agent.ToolResolve = before })
	wireTools(m.pg, nil)
	original, err := m.pg.GetTool("record_fact")
	if err != nil || original == nil {
		t.Fatal("missing fact tool", err)
	}
	t.Cleanup(func() {
		bindings, _ := json.Marshal(original.Agents)
		_ = m.pg.UpdateTool(original.Key, original.Description, original.Schema, bindings, original.Enabled)
	})
	custom := json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string","description":"preserve user summary"},"audit_id":{"type":"integer","default":42}},"required":["summary"]}`)
	if err := m.pg.UpdateTool("record_fact", "preserve user description", custom, json.RawMessage(`[]`), false); err != nil {
		t.Fatal(err)
	}
	wireTools(m.pg, nil)
	updated, err := m.pg.GetTool("record_fact")
	if err != nil || updated == nil {
		t.Fatal(err)
	}
	if updated.Description != "preserve user description" || updated.Enabled || len(updated.Agents) != 0 {
		t.Fatal("upgrade changed user visibility or description")
	}
	var schema map[string]any
	if err := json.Unmarshal(updated.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	for _, name := range []string{"observation_ids", "conclusion_kind", "context_key", "audit_id"} {
		if properties[name] == nil {
			t.Fatalf("missing optional property %s", name)
		}
	}
	if properties["summary"].(map[string]any)["description"] != "preserve user summary" || properties["audit_id"].(map[string]any)["default"] != float64(42) || len(schema["required"].([]any)) != 1 {
		t.Fatal("upgrade changed user schema")
	}
	if err := m.pg.UpdateTool("record_fact", updated.Description, updated.Schema, json.RawMessage(`["worker"]`), true); err != nil {
		t.Fatal(err)
	}
	tools := agent.ToolResolve(t.Context(), "worker", agent.NewToolSet(nil, "").WorkerTools())
	for _, tool := range tools {
		if tool.Name() == "record_fact" {
			visible := tool.InputSchema()["properties"].(map[string]any)
			for _, name := range []string{"observation_ids", "conclusion_kind", "context_key", "audit_id"} {
				if visible[name] == nil {
					t.Fatalf("runtime schema hides %s", name)
				}
			}
			return
		}
	}
	t.Fatal("enabled worker fact tool missing from runtime")
}
