package agent

import (
	"strings"
	"testing"
)

func TestOverviewCountsRealPostgres(t *testing.T) {
	d, es, ts, owner := factToolFixture(t)
	if err := es.SetIntentState(owner, "running"); err != nil {
		t.Fatal(err)
	}
	insert := func(kind, state string, count int) {
		t.Helper()
		_, err := d.Exec(`INSERT INTO exploration_nodes (exploration_id,kind,payload,state) SELECT exploration_id,$2,'{"summary":"overview fixture"}'::jsonb,$3 FROM exploration_nodes CROSS JOIN generate_series(1,$4) WHERE id=$1`, owner, kind, state, count)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("intent", "done", 301)
	first := ts.graphOverviewData()
	if first["running_intents_total"] != 1 || len(first["running_intents"].([]map[string]any)) != 1 || first["running_intents_truncated"] != false {
		t.Fatalf("old running intent missing: %#v", first)
	}
	insert("intent", "running", 300)
	insert("fact", "confirmed", 1001)
	insert("finding", "confirmed", 1001)
	out := ts.graphOverviewData()
	if out["running_intents_total"] != 301 || out["running_intents_truncated"] != true || len(out["running_intents"].([]map[string]any)) != 300 {
		t.Fatalf("running counts: %#v", out)
	}
	if out["facts"] != 1002 || out["findings_total"] != 1001 || len(out["recent_facts"].([]map[string]any)) != 20 || len(out["finding_list"].([]map[string]any)) != 10 {
		t.Fatalf("evidence counts: %#v", out)
	}
	if out["incomplete"] != false || out["observation_total"] != 0 {
		t.Fatalf("normal overview incomplete: %#v", out)
	}
}

func TestOverviewPartialQueriesRealPostgres(t *testing.T) {
	d, es, ts, owner := factToolFixture(t)
	if err := es.SetIntentState(owner, "running"); err != nil {
		t.Fatal(err)
	}
	if _, err := es.AddIntent(map[string]any{"summary": "queued"}, 1, nil, "test"); err != nil {
		t.Fatal(err)
	}
	// 视图函数按实际 SQL 触发异常，其余生产查询继续读取同一组真实节点。
	if _, err := d.Exec(`CREATE FUNCTION r31_overview_kind(value text) RETURNS text LANGUAGE plpgsql AS $$ BEGIN IF current_query() LIKE '%GROUP BY kind%' OR current_query() LIKE '%COUNT(*) OVER()%' OR current_query() LIKE '%COUNT(*)%state=''open''%' THEN RAISE EXCEPTION 'r31 selective overview failure'; END IF; RETURN value; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Exec(`DROP FUNCTION r31_overview_kind(text)`); err != nil {
			t.Error(err)
		}
	})
	if _, err := d.Exec(`ALTER TABLE exploration_nodes RENAME TO r31_overview_nodes`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Exec(`DROP VIEW IF EXISTS exploration_nodes; ALTER TABLE r31_overview_nodes RENAME TO exploration_nodes`); err != nil {
			t.Error(err)
		}
	})
	if _, err := d.Exec(`CREATE VIEW exploration_nodes AS SELECT id,exploration_id,r31_overview_kind(kind) AS kind,payload,priority,state,origin,owner,blocked_reason,delete_reason,created_at,updated_at,completed_at,content_version,cold_since_round FROM r31_overview_nodes`); err != nil {
		t.Fatal(err)
	}
	out := ts.graphOverviewData()
	for _, field := range []string{"facts", "findings_total", "observation_total", "validity_gate_total", "running_intents", "running_intents_total", "running_intents_truncated", "frontier_open"} {
		if out[field] != nil || !strings.Contains(out["query_errors"].(map[string]string)[field], "r31 selective overview failure") {
			t.Fatalf("%s must be unknown with SQL error: %#v", field, out)
		}
	}
	if out["incomplete"] != true || out["goals"] == nil || out["recent_facts"] == nil || out["open_intents"] == nil || out["done_intents_total"] != 0 {
		t.Fatalf("successful sections lost: %#v", out)
	}
}

func TestRenderGraphOverviewUncertaintyWarning(t *testing.T) {
	for _, data := range []map[string]any{{"incomplete": true}, {"facts": nil}, {"running_intents_truncated": true}} {
		got := renderGraphOverview(data)
		for _, text := range []string{"表示未知", "截断列表不代表完整集合", "不得依据未知值", "并非原子快照"} {
			if !strings.Contains(got, text) {
				t.Fatalf("missing code-owned warning %q: %s", text, got)
			}
		}
	}
}
