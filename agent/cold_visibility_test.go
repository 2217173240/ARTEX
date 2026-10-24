package agent

import (
	"strings"
	"testing"
)

func TestColdVisibilityReadFailureRealPostgres(t *testing.T) {
	d, es, ts, owner := factToolFixture(t)
	member, err := ts.recordOneFact(factItem{Summary: "covered fact"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := es.SetIntentState(owner, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := es.AddDigest(map[string]any{"summary": "cold fixture"}, []int64{owner, member}); err != nil {
		t.Fatal(err)
	}
	if !hiddenMembersFor(es)(member) {
		t.Fatal("cold covered fact must be hidden after successful graph read")
	}
	contains := func() bool {
		for _, item := range ts.graphOverviewData()["recent_facts"].([]map[string]any) {
			if item["id"] == member {
				return true
			}
		}
		return false
	}
	if contains() {
		t.Fatal("overview must fold cold covered fact")
	}
	// 实际视图保留正常列表与覆盖查询，只让全图节点读取发生数据库错误。
	if _, err := d.Exec(`CREATE FUNCTION r30_cold_priority(value integer) RETURNS integer LANGUAGE plpgsql AS $$ BEGIN IF current_query() LIKE '%ORDER BY id LIMIT $2%' THEN RAISE EXCEPTION 'r30 full graph read rejected'; END IF; RETURN value; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Exec(`DROP FUNCTION r30_cold_priority(integer)`); err != nil {
			t.Error(err)
		}
	})
	if _, err := d.Exec(`ALTER TABLE exploration_nodes RENAME TO r30_cold_nodes`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Exec(`DROP VIEW IF EXISTS exploration_nodes; ALTER TABLE r30_cold_nodes RENAME TO exploration_nodes`); err != nil {
			t.Error(err)
		}
	})
	if _, err := d.Exec(`CREATE VIEW exploration_nodes AS SELECT id,exploration_id,kind,payload,r30_cold_priority(priority) AS priority,state,origin,owner,blocked_reason,delete_reason,created_at,updated_at,completed_at,content_version,cold_since_round FROM r30_cold_nodes`); err != nil {
		t.Fatal(err)
	}
	covered, err := es.CoveredMembers()
	if err != nil || covered[member] == 0 {
		t.Fatalf("coverage query failed: %v %v", covered, err)
	}
	if _, _, err := loadColdGraph(es); err == nil || !strings.Contains(err.Error(), "r30 full graph read rejected") {
		t.Fatalf("expected real graph read failure: %v", err)
	}
	if hiddenMembersFor(es)(member) {
		t.Fatal("graph read failure hid a covered fact")
	}
	if !contains() {
		t.Fatal("overview omitted covered fact after graph read failure")
	}
}
