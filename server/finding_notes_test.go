package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/google/uuid"
)

func isolatedNotesServer(t *testing.T) (*Server, *db.RecordedFinding, func(string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	raw := os.Getenv("ARTEX_NOTES_TEST_ADMIN_DSN")
	if raw == "" {
		t.Skip("ARTEX_NOTES_TEST_ADMIN_DSN required for isolated notes API integration database")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	name := "artex_r9_notes_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`); admin.Close() })
	u.Path = "/" + name
	t.Setenv("ARTEX_PG_DSN", u.String())
	return trafficEvidenceServer(t)
}

func TestFindingNotesAPISharedActorAuthAndGuards(t *testing.T) {
	s, f, request := isolatedNotesServer(t)
	path := fmt.Sprintf("/api/exploration/findings/%d/notes", f.FindingID)
	for _, method := range []string{"GET", "POST", "DELETE"} {
		p := path
		if method == "DELETE" {
			p += "/1"
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(method, p, strings.NewReader(`{"body":"unauthenticated"}`)))
		if w.Code != 401 {
			t.Fatalf("%s notes route auth status %d %s", method, w.Code, w.Body)
		}
	}
	w := request("POST", path, `{"body":"  人工补充\n ","author":"spoofed operator","reviewed_by":"spoof"}`)
	if w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	var note FindingNoteDTO
	if err := json.Unmarshal(w.Body.Bytes(), &note); err != nil {
		t.Fatal(err)
	}
	if note.Author != findingManualActor || note.Body != "人工补充" || note.CreatedAt == "" {
		t.Fatalf("untrusted author or untrimmed body %+v", note)
	}
	for _, body := range []string{`{"body":" \n\t "}`, `{"body":"` + strings.Repeat("中", 8001) + `"}`} {
		if w = request("POST", path, body); w.Code != 400 {
			t.Fatalf("invalid body %d %s", w.Code, w.Body)
		}
	}
	source, _ := s.m.pg.GetFinding(f.FindingID)
	child, err := s.m.pg.CreateTaskWithOptions("notes child", "fixture", db.TaskCreateOptions{SourceTaskIDs: []int64{*source.TaskID}})
	if err != nil {
		t.Fatal(err)
	}
	inherited := path + fmt.Sprintf("?context_task=%d", child.ID)
	if w = request("GET", inherited, ""); w.Code != 200 {
		t.Fatalf("inherited read %d %s", w.Code, w.Body)
	}
	if w = request("POST", inherited, `{"body":"write"}`); w.Code != 403 {
		t.Fatalf("inherited write %d %s", w.Code, w.Body)
	}
	if w = request("DELETE", path+"/"+note.ID+fmt.Sprintf("?context_task=%d", child.ID), ""); w.Code != 403 {
		t.Fatalf("inherited delete %d %s", w.Code, w.Body)
	}
	findingPath := fmt.Sprintf("/api/exploration/findings/%d", f.FindingID)
	if w = request("PATCH", findingPath+fmt.Sprintf("?context_task=%d", child.ID), `{"status":"confirmed"}`); w.Code != 403 {
		t.Fatalf("inherited status %d %s", w.Code, w.Body)
	}
	if w = request("PATCH", findingPath, `{"status":"confirmed","severity":"invalid"}`); w.Code != 400 {
		t.Fatalf("invalid patch %d %s", w.Code, w.Body)
	}
	current, _ := s.m.pg.GetFinding(f.FindingID)
	if current.Status != db.FindingPending || current.ReviewedAt != nil {
		t.Fatalf("invalid patch partially committed %+v", current)
	}
	if w = request("PATCH", findingPath, `{"status":"confirmed","reviewed_by":"spoofed"}`); w.Code != 200 {
		t.Fatalf("manual patch %d %s", w.Code, w.Body)
	}
	var dto FindingDTO
	if err = json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.ReviewedBy != findingManualActor || dto.ReviewedAt == "" || dto.ReviewedStatus != db.FindingConfirmed {
		t.Fatalf("manual review DTO %+v", dto)
	}

	meta, err := s.m.pg.FindingMetaByNodeID(*source.TaskID)
	if err != nil || meta[f.NodeID].ReviewedBy != findingManualActor || meta[f.NodeID].ReviewedStatus != db.FindingConfirmed {
		t.Fatalf("task finding review metadata %+v %v", meta, err)
	}
	if w = request("GET", "/api/exploration/findings?task="+fmt.Sprint(*source.TaskID), ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"reviewed_status":"confirmed"`) {
		t.Fatalf("task finding review DTO %d %s", w.Code, w.Body)
	}
	if w = request("GET", path+"?limit=1", ""); w.Code != 200 {
		t.Fatalf("notes read %d %s", w.Code, w.Body)
	}
	if w = request("DELETE", path+"/"+note.ID, ""); w.Code != 204 {
		t.Fatalf("delete %d %s", w.Code, w.Body)
	}
	if w = request("DELETE", path+"/"+note.ID, ""); w.Code != 404 {
		t.Fatalf("repeat delete %d %s", w.Code, w.Body)
	}
	if w = request("GET", "/api/exploration/findings/999999999/notes", ""); w.Code != 404 {
		t.Fatalf("missing finding %d %s", w.Code, w.Body)
	}
	// Queueing freezes the source finding, while note reads remain available.
	s.m.pg.Exec(`DELETE FROM task_relations WHERE task_id=$1`, child.ID)
	s.m.pg.Exec(`UPDATE tasks SET paused=true WHERE id=$1`, *source.TaskID)
	if _, err = s.m.pg.QueueTaskArchive(*source.TaskID); err != nil {
		t.Fatal(err)
	}
	if w = request("POST", path, `{"body":"after queue"}`); w.Code != 409 {
		t.Fatalf("archived note write %d %s", w.Code, w.Body)
	}
	if w = request("PATCH", findingPath, `{"status":"fixed"}`); w.Code != 409 {
		t.Fatalf("archived status write %d %s", w.Code, w.Body)
	}
	if w = request("GET", path, ""); w.Code != 200 {
		t.Fatalf("archived notes read %d %s", w.Code, w.Body)
	}
}
