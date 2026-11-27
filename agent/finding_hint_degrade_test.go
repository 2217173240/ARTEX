package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

type capturingRecorder struct {
	got []db.TrafficRef
}

func (c *capturingRecorder) Record(_ context.Context, _ db.RecordFindingInput, refs []db.TrafficRef) (*db.RecordedFinding, error) {
	c.got = append([]db.TrafficRef(nil), refs...)
	bindings := make([]db.FindingTrafficBinding, len(refs))
	for i := range refs {
		bindings[i] = db.FindingTrafficBinding{ID: int64(i + 1), Role: refs[i].Role, Note: refs[i].Note}
	}
	return &db.RecordedFinding{
		FindingID: 1,
		NodeID:    1,
		Traffic:   &db.FindingTraffic{Bindings: bindings},
	}, nil
}

// 不依赖 PG：验证 hintID<=0（缺省/0/负数）直接按显式 traffic_refs 规范化，不访问数据库。
func TestFindingRefsFromHintZeroOrNegative(t *testing.T) {
	ts := NewToolSet(nil, "")
	for _, hintID := range []int64{0, -1, -5} {
		refs, err := ts.findingRefsFromHint(hintID, nil)
		if err != nil || len(refs) != 0 {
			t.Fatalf("hintID=%d: got refs=%+v err=%v, want empty nil", hintID, refs, err)
		}
		refs, err = ts.findingRefsFromHint(hintID, []db.TrafficRef{{TrafficID: "tr_1", Role: "proof"}})
		if err != nil || len(refs) != 1 || refs[0].TrafficID != "tr_1" {
			t.Fatalf("hintID=%d with explicit refs: got refs=%+v err=%v", hintID, refs, err)
		}
	}
}

// 不依赖 PG：验证 errEvidenceHintNotFound 降级逻辑（空显式引用、有效显式引用、无效显式引用与非 hint 错误）。
func TestResolveFindingTrafficRefsDegradesWithoutDB(t *testing.T) {
	staleLookup := func(hintID int64, _ []db.TrafficRef) ([]db.TrafficRef, error) {
		return nil, fmt.Errorf("%w: evidence_hint_id=%d", errEvidenceHintNotFound, hintID)
	}
	refs, note, err := resolveFindingTrafficRefs(999, nil, staleLookup)
	if err != nil || len(refs) != 0 || !strings.Contains(note, "已忽略过期或非本任务的 evidence_hint_id=999") {
		t.Fatalf("stale empty refs: refs=%+v note=%q err=%v", refs, note, err)
	}

	refs, note, err = resolveFindingTrafficRefs(999, []db.TrafficRef{{TrafficID: "tr_2", Role: ""}}, staleLookup)
	if err != nil || len(refs) != 1 || refs[0].Role != "supporting" || !strings.Contains(note, "evidence_hint_id=999") {
		t.Fatalf("stale valid refs: refs=%+v note=%q err=%v", refs, note, err)
	}

	if _, _, err = resolveFindingTrafficRefs(999, []db.TrafficRef{{TrafficID: ""}}, staleLookup); err == nil {
		t.Fatalf("expected invalid explicit traffic_refs to fail normalization")
	}

	dbErr := errors.New("connection lost")
	_, _, err = resolveFindingTrafficRefs(999, nil, func(int64, []db.TrafficRef) ([]db.TrafficRef, error) {
		return nil, dbErr
	})
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected dbErr to propagate, got %v", err)
	}
}

func TestReportFindingNonHintNodeDegradesToExplicitRefs(t *testing.T) {
	prev := FindingTrafficBindingEnabled
	FindingTrafficBindingEnabled = func() bool { return true }
	t.Cleanup(func() { FindingTrafficBindingEnabled = prev })

	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("non-hint degrade", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = d.DeleteFindingsByTask(task.ID)
		_ = d.DeleteTask(task.ID)
	}()
	exp := d.Exploration(task.ExplorationID)
	factID, err := exp.AddNode(db.KindFact, map[string]any{"summary": "not a hint"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}

	rec := &capturingRecorder{}
	ts := NewToolSet(nil, "")
	ts.ts, ts.taskID = exp, task.ID
	ts.SetFindingRecorder(rec)

	body, _ := json.Marshal(map[string]any{
		"vulnclass":        "IDOR",
		"severity":         "high",
		"summary":          "non-hint with explicit refs",
		"evidence":         "GET /api/user/2",
		"evidence_hint_id": factID,
		"traffic_refs":     []db.TrafficRef{{TrafficID: "tr_proof_1", Role: "proof", Note: "leak"}},
	})
	res, err := ts.addFinding().Call(t.Context(), json.RawMessage(body), nil)
	if err != nil {
		t.Fatalf("report_finding returned error: %v", err)
	}
	got := res.Flatten()
	if !strings.HasPrefix(got, "finding recorded: ") {
		t.Fatalf("non-hint evidence_hint_id rejected finding: %q", got)
	}
	if !strings.Contains(got, "已忽略过期或非本任务的 evidence_hint_id=") {
		t.Fatalf("expected explanatory evidence_note, got: %q", got)
	}
	if len(rec.got) != 1 || rec.got[0].TrafficID != "tr_proof_1" {
		t.Fatalf("explicit proof ref did not survive: %+v", rec.got)
	}
}

// Regression: 省略、null、空字符串、0 或负数的 evidence_hint_id 在未传 traffic_refs
// 时也必须成功登记（默认由报告 Agent 后续绑定流量）。
func TestReportFindingAbsentOrZeroHintIDWithoutTrafficRefs(t *testing.T) {
	prev := FindingTrafficBindingEnabled
	FindingTrafficBindingEnabled = func() bool { return true }
	t.Cleanup(func() { FindingTrafficBindingEnabled = prev })

	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("absent hint id", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = d.DeleteFindingsByTask(task.ID)
		_ = d.DeleteTask(task.ID)
	}()

	cases := []struct {
		name string
		body string
	}{
		{"omitted", `{"vulnclass":"SQLI","severity":"high","summary":"omitted hint","evidence":"UNION rows"}`},
		{"zero", `{"vulnclass":"SQLI","severity":"high","summary":"zero hint","evidence":"UNION rows","evidence_hint_id":0}`},
		{"null", `{"vulnclass":"SQLI","severity":"high","summary":"null hint","evidence":"UNION rows","evidence_hint_id":null}`},
		{"empty_string", `{"vulnclass":"SQLI","severity":"high","summary":"empty hint","evidence":"UNION rows","evidence_hint_id":""}`},
		{"negative", `{"vulnclass":"SQLI","severity":"high","summary":"negative hint","evidence":"UNION rows","evidence_hint_id":-5}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := NewToolSet(nil, "")
			ts.ts, ts.taskID = d.Exploration(task.ExplorationID), task.ID
			rec := &capturingRecorder{}
			ts.SetFindingRecorder(rec)

			res, err := ts.addFinding().Call(t.Context(), json.RawMessage(tc.body), nil)
			if err != nil {
				t.Fatalf("report_finding returned error: %v", err)
			}
			if got := res.Flatten(); !strings.HasPrefix(got, "finding recorded: ") {
				t.Fatalf("%s rejected: %q", tc.name, got)
			}
			if len(rec.got) != 0 {
				t.Fatalf("%s unexpectedly bound refs: %+v", tc.name, rec.got)
			}
		})
	}
}

// Regression: 过期/不存在或非提示节点的 evidence_hint_id 在未传 traffic_refs 时
// 必须优雅降级（记录为 not_bound 并附说明），不能丢弃已确认漏洞。
func TestReportFindingStaleOrNonHintIDWithoutTrafficRefsDegradesCleanly(t *testing.T) {
	prev := FindingTrafficBindingEnabled
	FindingTrafficBindingEnabled = func() bool { return true }
	t.Cleanup(func() { FindingTrafficBindingEnabled = prev })

	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("stale hint id", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = d.DeleteFindingsByTask(task.ID)
		_ = d.DeleteTask(task.ID)
	}()
	exp := d.Exploration(task.ExplorationID)
	factID, err := exp.AddNode(db.KindFact, map[string]any{"summary": "not a hint"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		hintID int64
	}{
		{"stale_nonexistent", 999999},
		{"non_hint_fact_node", factID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &capturingRecorder{}
			ts := NewToolSet(nil, "")
			ts.ts, ts.taskID = exp, task.ID
			ts.SetFindingRecorder(rec)

			body, _ := json.Marshal(map[string]any{
				"vulnclass":        "XSS",
				"severity":         "medium",
				"summary":          "stale hint finding",
				"evidence":         "reflected payload",
				"evidence_hint_id": tc.hintID,
			})
			res, err := ts.addFinding().Call(t.Context(), json.RawMessage(body), nil)
			if err != nil {
				t.Fatalf("report_finding returned error: %v", err)
			}
			got := res.Flatten()
			if !strings.HasPrefix(got, "finding recorded: ") {
				t.Fatalf("stale/non-hint evidence_hint_id discarded finding: %q", got)
			}
			if !strings.Contains(got, "已忽略过期或非本任务的 evidence_hint_id=") {
				t.Fatalf("expected explanatory evidence_note, got: %q", got)
			}
			if !strings.Contains(got, `"evidence_status":"not_bound"`) {
				t.Fatalf("expected not_bound status, got: %q", got)
			}
			if len(rec.got) != 0 {
				t.Fatalf("expected 0 refs on degraded stale hint, got %+v", rec.got)
			}
		})
	}
}
