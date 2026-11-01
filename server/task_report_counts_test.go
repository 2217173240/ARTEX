package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func reportCountsServer(t *testing.T) (*Server, *Task, func(string, bool) (int, string)) {
	t.Helper()
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("explicit isolated ARTEX_PG_DSN required")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("report_counts_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`); err != nil {
			t.Errorf("drop fixture database: %v", err)
		}
		admin.Close()
	})
	u.Path = "/" + name
	pg, err := db.Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close() })
	source, err := pg.CreateTask("source report fixture", "source", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exploration(source.ExplorationID).AddNode(db.KindFinding, map[string]any{"name": "excluded-source-marker"}, 0, "confirmed", "test", nil); err != nil {
		t.Fatal(err)
	}
	current, err := pg.CreateTaskWithOptions("report count integration", "all findings", db.TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	task := &Task{ID: strconv.FormatInt(current.ID, 10), ExpID: current.ExplorationID, Store: pg.Exploration(current.ExplorationID), Description: current.Description, SourceTaskIDs: current.SourceTaskIDs}
	m := &Manager{pg: pg, assets: pg.Assets(), tasks: map[string]*Task{task.ID: task}, active: task.ID}
	s := &Server{m: m, jwtKey: []byte("report-count-integration-signing-key")}
	s.engine = NewEngine(m)
	token, err := signAdminJWTForTest(t, s)
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(s.Handler())
	t.Cleanup(h.Close)
	return s, task, func(path string, auth bool) (int, string) {
		t.Helper()
		r, err := http.NewRequest("GET", h.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := h.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, string(b)
	}
}

func TestTaskCountsExactHTTPAndFallback(t *testing.T) {
	s, task, request := reportCountsServer(t)
	for _, seed := range []struct {
		kind, state string
		count       int
	}{
		{db.KindIntent, "running", 100001}, {db.KindIntent, "open", 1001}, {db.KindIntent, "done", 3},
		{db.KindGoal, "met", 10001}, {db.KindGoal, "open", 2}, {db.KindGoal, "abandoned", 1},
		{db.KindFinding, "confirmed", 1001}, {db.KindFinding, "dismissed", 2}, {db.KindFact, "confirmed", 4},
	} {
		if _, err := s.m.pg.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,state,payload) SELECT $1,$2,$3,'{}'::jsonb FROM generate_series(1,$4)`, task.ExpID, seed.kind, seed.state, seed.count); err != nil {
			t.Fatal(err)
		}
	}
	other, err := s.m.pg.CreateExploration("other counts", "isolation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.m.pg.Exploration(other).AddIntent(map[string]any{}, 0, nil, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,state) VALUES($1,'intent','running'),($1,'goal','met'),($1,'finding','confirmed')`, other); err != nil {
		t.Fatal(err)
	}
	code, body := request("/api/stats?task="+task.ID, true)
	var result struct {
		Exploration map[string]int `json:"exploration"`
		Active      struct {
			InFlight   int `json:"in_flight"`
			GoalsTotal int `json:"goals_total"`
			GoalsMet   int `json:"goals_met"`
		} `json:"active_task"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err, body)
	}
	if code != 200 || result.Active.InFlight != 100001 || result.Active.GoalsTotal != 10004 || result.Active.GoalsMet != 10001 || result.Exploration[db.KindIntent] != 101005 || result.Exploration[db.KindFinding] != 1003 {
		t.Fatalf("stats %d %+v", code, result)
	}
	if reply := s.fallbackChat(context.Background(), task, "status"); !strings.Contains(reply, "待领意图 1001，确认发现 1003") {
		t.Fatal(reply)
	}
}

func TestTaskReportGlobalAssetCountsHTTP(t *testing.T) {
	s, task, request := reportCountsServer(t)
	if code, body := request("/api/report?task="+task.ID, true); code != 200 || !strings.Contains(body, "- 资产：\n") {
		t.Fatalf("empty assets %d %s", code, body)
	}
	for _, typ := range []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"} {
		if _, err := s.m.pg.Exec(`INSERT INTO assets(type,task_ids) VALUES($1,$2),($1,'{}')`, typ, "{999999}"); err != nil {
			t.Fatal(err)
		}
	}
	code, body := request("/api/report?task="+task.ID, true)
	if code != 200 {
		t.Fatal(code, body)
	}
	for _, typ := range []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"} {
		if !strings.Contains(body, typ+" 2") {
			t.Fatalf("missing global %s count: %s", typ, body)
		}
	}
}

func TestTaskReportCountFailuresHTTP(t *testing.T) {
	for _, table := range []string{"exploration_nodes", "assets"} {
		t.Run(table, func(t *testing.T) {
			s, task, request := reportCountsServer(t)
			if _, err := s.m.pg.Exec(`ALTER TABLE ` + table + ` RENAME TO unavailable_` + table); err != nil {
				t.Fatal(err)
			}
			code, body := request("/api/report?task="+task.ID, true)
			if code != 500 || strings.Contains(body, "# 渗透测试报告") {
				t.Fatalf("report failure %d %s", code, body)
			}
			if reply := s.fallbackChat(context.Background(), task, "status"); !strings.Contains(reply, "暂时无法读取") || strings.Contains(reply, "资产 0") {
				t.Fatal(reply)
			}
			code, body = request("/api/stats?task="+task.ID, true)
			if table == "exploration_nodes" && code != 500 {
				t.Fatalf("stats failure %d %s", code, body)
			}
			if table == "assets" && (code != 200 || strings.Contains(body, `"asset_counts"`) || strings.Contains(body, `"assets"`)) {
				t.Fatalf("optional assets %d %s", code, body)
			}
			if code, _ := request("/api/auth/session", true); code != 200 {
				t.Fatalf("independent session unavailable: %d", code)
			}
		})
	}
}

func TestTaskReportCompleteFindingsHTTP(t *testing.T) {
	s, task, request := reportCountsServer(t)
	_, err := s.m.pg.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,payload,state,origin)
SELECT $1,'finding',jsonb_build_object('name','report-marker-'||g, 'summary','fixture evidence'),
CASE WHEN g%2=0 THEN 'dismissed' ELSE 'confirmed' END,'test'
FROM generate_series(1,1001) g`, task.ExpID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = task.Store.AddNode(db.KindFact, map[string]any{"name": "excluded-fact-marker"}, 0, "confirmed", "test", nil); err != nil {
		t.Fatal(err)
	}
	other, err := s.m.pg.CreateExploration("foreign", "foreign")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.m.pg.Exec(`DELETE FROM explorations WHERE id=$1`, other) })
	if _, err = s.m.pg.Exploration(other).AddNode(db.KindFinding, map[string]any{"name": "excluded-foreign-marker"}, 0, "confirmed", "test", nil); err != nil {
		t.Fatal(err)
	}
	path := "/api/report?task=" + task.ID
	if code, _ := request(path, false); code != 401 {
		t.Fatalf("unauthenticated status %d", code)
	}
	if code, _ := request("/api/report?task=999999999999", true); code != 404 {
		t.Fatalf("missing task status %d", code)
	}
	code, body := request(path, true)
	oldest := strings.Contains(body, "report-marker-1\n")
	newest := strings.Contains(body, "report-marker-1001\n")
	if code != 200 || !strings.Contains(body, "确认发现：**1001**") || strings.Count(body, "### ") != 1001 || !oldest || !newest {
		t.Fatalf("report status=%d sections=%d oldest=%v newest=%v expected 1001", code, strings.Count(body, "### "), oldest, newest)
	}
	for _, marker := range []string{"excluded-fact-marker", "excluded-foreign-marker", "excluded-source-marker"} {
		if strings.Contains(body, marker) {
			t.Fatalf("report included %s", marker)
		}
	}
}

func TestTaskReportQueriesCancellation(t *testing.T) {
	_, task, _ := reportCountsServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := task.Store.ReportFindings(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("report cancellation: %v", err)
	}
	if _, err := task.Store.TaskCounts(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("counts cancellation: %v", err)
	}
}
