package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

func TestInterceptSearchFilterParams(t *testing.T) {
	filter, err := interceptFilterParams(url.Values{
		"q":            {"  审批 https://example.test/a?x=1  "},
		"created_from": {"2026-10-01"}, "created_to": {"2026-10-12"},
		"decision_source": {"manual"},
	})
	if err != nil || filter.Search != "审批 https://example.test/a?x=1" || filter.DecisionSource != "manual" {
		t.Fatalf("filter=%+v err=%v", filter, err)
	}
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	if filter.CreatedFrom == nil || !filter.CreatedFrom.Equal(from) || filter.CreatedTo == nil || !filter.CreatedTo.Equal(to) {
		t.Fatalf("date bounds are not inclusive UTC days: %+v", filter)
	}
	for _, text := range []string{strings.Repeat("中", 200), "#1", "#9223372036854775807"} {
		if _, err := interceptFilterParams(url.Values{"q": {text}}); err != nil {
			t.Fatalf("valid search rejected: %v", err)
		}
	}
	for _, values := range []url.Values{
		{"q": {strings.Repeat("中", 201)}}, {"q": {string([]byte{0xff})}},
		{"q": {"#0"}}, {"q": {"#01"}}, {"q": {"#+1"}}, {"q": {"#-1"}},
		{"q": {"#abc"}}, {"q": {"#1.0"}}, {"q": {"#9223372036854775808"}},
		{"created_from": {"2026-10-1"}}, {"created_to": {"2026-02-29"}},
		{"created_from": {"2026-10-13"}, "created_to": {"2026-10-12"}},
	} {
		if _, err := interceptFilterParams(values); err == nil {
			t.Fatalf("invalid filter accepted: %v", values)
		}
	}
	filter, err = interceptFilterParams(url.Values{"q": {"  "}})
	if err != nil || filter != (db.InterceptApprovalFilter{}) {
		t.Fatalf("blank search changed legacy route: %+v %v", filter, err)
	}
}

func TestInterceptFilterHTTP(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("no test database configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	m := &Manager{pg: d, interceptor: intercept.New(d)}
	s := &Server{m: m, jwtKey: []byte("approval-filter-test-key-32bytes-fixture")}
	h := s.Handler()
	token, err := signAdminJWTForTest(t, s)
	if err != nil {
		t.Fatal(err)
	}
	scope := "approval-http-filter"
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_pending WHERE task_id IN ($1,$2)`, scope, scope+"-other") })
	_, baseline, err := d.ListAllInterceptsPage(1, 20, db.InterceptApprovalFilter{Status: "denied", DecisionSource: "model"})
	if err != nil {
		t.Fatal(err)
	}
	var deniedID int64
	for _, state := range []string{"pending", "allowed", "denied", "timeout"} {
		id, err := d.CreateDecidedIntercept(0, 0, scope, "test", "Bash", []byte(`{}`), state, "[模型] fixture")
		if err != nil {
			t.Fatal(err)
		}
		if state == "denied" {
			deniedID = id
		}
	}
	if _, err := d.CreateDecidedIntercept(0, 0, scope+"-other", "test", "Bash", []byte(`{}`), "denied", "[模型] fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE intercept_pending SET created_at='2026-10-12 23:59:59.999999+00' WHERE task_id=$1`, scope); err != nil {
		t.Fatal(err)
	}
	do := func(path string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, base := range []string{"/api/intercept/history", "/api/intercept/task/" + scope} {
		if rec := do(base+"?status=denied&decision_source=model", false); rec.Code != 401 {
			t.Fatalf("unprotected list: %d", rec.Code)
		}
		for _, query := range []string{"?status=invalid", "?decision_source=invalid", "?page=1&status=denied%27+OR+1%3D1--", "?size=20&decision_source=MODEL", "?q=%23abc", "?q=" + url.QueryEscape(strings.Repeat("中", 201)), "?created_from=2026-10-1", "?created_to=2026-02-29", "?created_from=2026-10-13&created_to=2026-10-12"} {
			if rec := do(base+query, true); rec.Code != 400 {
				t.Fatalf("invalid filter %s: %d %s", query, rec.Code, rec.Body.String())
			}
		}
		// Filtering without explicit page parameters must still apply the filters.
		rec := do(base+"?status=denied&decision_source=model", true)
		var body struct {
			Items []db.InterceptApprovalRow `json:"items"`
			Total int                       `json:"total"`
			Page  int                       `json:"page"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		want := baseline + 2
		if base != "/api/intercept/history" {
			want = 1
		}
		if body.Total != want || body.Page != 1 {
			t.Fatalf("filter total=%d want=%d page=%d", body.Total, want, body.Page)
		}
		for _, r := range body.Items {
			if r.Status != "denied" || r.DecisionSource != "model" {
				t.Fatalf("nonmatching item: %+v", r)
			}
		}
	}
	for _, tc := range []struct {
		query             string
		total, rows, page int
	}{
		{"", 4, 4, 0},
		{"?page=2&size=2&decision_source=model", 4, 2, 2},
		{"?page=2&size=1&status=denied&decision_source=model", 1, 0, 2},
		{"?status=denied&decision_source=unknown", 0, 0, 1},
		{"?page=0&size=0&status=pending", 1, 1, 1},
		{"?q=fixture", 4, 4, 1},
		{"?q=" + url.QueryEscape(fmt.Sprintf(" #%d ", deniedID)), 1, 1, 1},
		{"?q=fixture&created_from=2026-10-12&created_to=2026-10-12", 4, 4, 1},
		{"?created_to=2026-10-11", 0, 0, 1},
		{"?created_from=2026-10-13", 0, 0, 1},
	} {
		rec := do("/api/intercept/task/"+scope+tc.query, true)
		var body struct {
			Items []db.InterceptApprovalRow `json:"items"`
			Total int                       `json:"total"`
			Page  int                       `json:"page"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Total != tc.total || len(body.Items) != tc.rows || body.Page != tc.page {
			t.Fatalf("%s: %d %s", tc.query, rec.Code, rec.Body.String())
		}
	}
}
