package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestProveGoalRealPostgres(t *testing.T) {
	for _, count := range []int{1, 2, 1001} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			_, es, ts, _ := factToolFixture(t)
			goals := make([]int64, count)
			for i := range goals {
				id, err := es.AddGoal(map[string]any{"text": fmt.Sprint(i)}, "test")
				if err != nil {
					t.Fatal(err)
				}
				goals[i] = id
				if i > 0 && i < count-1 {
					if err := es.SetNodeState(id, "met"); err != nil {
						t.Fatal(err)
					}
				}
			}
			fact, err := ts.recordOneFact(factItem{Summary: "evidence"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			target := goals[count-1]
			got := evidenceCall(t, ts.proveGoal(), map[string]any{"goal_id": target, "evidence_id": fact}, false)
			if ts.GoalMet != (count == 1) {
				t.Fatalf("GoalMet=%v count=%d receipt=%s", ts.GoalMet, count, got)
			}
			if count == 1 && (!strings.Contains(got, "任务自动判定完成") || ts.Reason == "") {
				t.Fatal(got)
			}
			if count > 1 && (got != fmt.Sprintf("goal %d marked met", target) || ts.Reason != "") {
				t.Fatal(got, ts.Reason)
			}
			if count > 1 {
				evidenceCall(t, ts.proveGoal(), map[string]any{"goal_id": goals[0], "evidence_id": fact}, false)
				if !ts.GoalMet {
					t.Fatal("all goals met without completion")
				}
			}
		})
	}
}

func TestProveGoalWriteFailureRealPostgres(t *testing.T) {
	d, es, ts, _ := factToolFixture(t)
	goal, err := es.AddGoal(map[string]any{"text": "trigger failure"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	fact, err := ts.recordOneFact(factItem{Summary: "evidence"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := fmt.Sprintf("r30_goal_fail_%d", goal)
	if _, err := d.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id=%d AND NEW.state='met' THEN RAISE EXCEPTION 'r30 goal write rejected'; END IF; RETURN NEW; END $$`, fn, goal)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Exec("DROP FUNCTION " + fn + "() CASCADE"); err != nil {
			t.Error(err)
		}
	})
	if _, err := d.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE ON exploration_nodes FOR EACH ROW EXECUTE FUNCTION %s()`, fn, fn)); err != nil {
		t.Fatal(err)
	}
	ts.Reason = "preserve reason"
	got := evidenceCall(t, ts.proveGoal(), map[string]any{"goal_id": goal, "evidence_id": fact}, true)
	if !strings.Contains(got, "r30 goal write rejected") || ts.GoalMet || ts.Reason != "preserve reason" {
		t.Fatal(got, ts.GoalMet, ts.Reason)
	}
	node, err := es.GetNode(goal)
	if err != nil {
		t.Fatal(err)
	}
	if node.State != "open" {
		t.Fatal(node.State)
	}
	var edges int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE src_id=$1 AND dst_id=$2 AND rel='proves'`, fact, goal).Scan(&edges); err != nil {
		t.Fatal(err)
	}
	if edges != 0 {
		t.Fatal("proof edge survived rollback")
	}
	for _, input := range []string{`{`, `{"goal_id":0,"evidence_id":1}`, `{"goal_id":1,"evidence_id":0}`} {
		r, err := ts.proveGoal().Call(t.Context(), json.RawMessage(input), nil)
		if err != nil || !r.IsError {
			t.Fatalf("accepted %s: %v %s", input, err, r.Flatten())
		}
	}
}
