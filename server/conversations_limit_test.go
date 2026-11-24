package server

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestConversationMessagesBoundedPages(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	c, err := pg.CreateConversation("mainagent", "message page bounds", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.DeleteConversation(c.ID) })
	if _, err := pg.Exec(`INSERT INTO conversation_activities(conversation_id,worker,kind,summary)
		SELECT $1,'mainagent','text','message page' FROM generate_series(1,1001)`, c.ID); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg}}
	type page struct {
		Items   []ActivityDTO `json:"items"`
		Cursor  int64         `json:"cursor"`
		Running bool          `json:"running"`
		HasMore bool          `json:"hasMore"`
	}
	get := func(query string) page {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/conversations/"+strconv.FormatInt(c.ID, 10)+"/messages?"+query, nil)
		r.SetPathValue("id", strconv.FormatInt(c.ID, 10))
		w := httptest.NewRecorder()
		s.pgConversationMessages(w, r)
		if w.Code != 200 {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body)
		}
		var p page
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, limit := range []string{"", "invalid", "-1", "0", "200", "201"} {
		p := get("limit=" + limit)
		if len(p.Items) != 200 || !p.HasMore || p.Running {
			t.Fatalf("history limit=%q rows=%d more=%v running=%v", limit, len(p.Items), p.HasMore, p.Running)
		}
	}
	p := get("since=0&limit=1001")
	if len(p.Items) != 1000 || p.Cursor <= 0 || !p.HasMore || p.Running {
		t.Fatalf("tail first: rows=%d cursor=%d more=%v running=%v", len(p.Items), p.Cursor, p.HasMore, p.Running)
	}
	exact := get("since=" + strconv.FormatInt(p.Items[0].Seq, 10))
	if len(exact.Items) != 1000 || exact.HasMore || exact.Running {
		t.Fatalf("tail exact page: rows=%d more=%v running=%v", len(exact.Items), exact.HasMore, exact.Running)
	}
	last := get("since=" + strconv.FormatInt(p.Cursor, 10))
	if len(last.Items) != 1 || last.Cursor <= p.Cursor || last.HasMore || last.Running {
		t.Fatalf("tail last: rows=%d cursor=%d more=%v running=%v", len(last.Items), last.Cursor, last.HasMore, last.Running)
	}
	empty := get("since=" + strconv.FormatInt(last.Cursor, 10))
	if len(empty.Items) != 0 || empty.Cursor != last.Cursor || empty.HasMore {
		t.Fatalf("tail exhausted: rows=%d cursor=%d more=%v", len(empty.Items), empty.Cursor, empty.HasMore)
	}
}
