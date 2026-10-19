package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func evidenceCall(t *testing.T, tool actool.CoreTool, input any, wantError bool) string {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r, err := tool.Call(t.Context(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError != wantError {
		t.Fatalf("%s error=%v result=%s", tool.Name(), r.IsError, r.Flatten())
	}
	return r.Flatten()
}

func evidenceNode(t *testing.T, raw string) *db.Node {
	t.Helper()
	var n db.Node
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return &n
}

// Executes the real local Bash tool, persists its runtime-shaped receipt, then
// calls the production evidence tools against PostgreSQL. No network or model.
func TestEvidenceToolsRealExecutionAndClaimBinding(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("evidence fixture", "local output", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	es := d.Exploration(task.ExplorationID)
	intent, err := es.AddIntent(map[string]any{"summary": "local tool execution"}, 1, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	ts := NewToolSet(es, "test")
	ts.SetTaskID(task.ID)
	ts.SetOwnerNode(intent)
	input := json.RawMessage(`{"command":"printf 'local evidence result\\n'"}`)
	result, err := actool.NewBash().Call(t.Context(), input, nil)
	if err != nil || result.IsError {
		t.Fatalf("real Bash failed %v %s", err, result.Flatten())
	}
	if _, err = es.AppendActivity(db.Activity{NodeID: &intent, Worker: "test", Kind: "tool_use", Tool: "Bash", ToolUseID: "real-bash", Detail: string(input)}); err != nil {
		t.Fatal(err)
	}
	act, err := es.AppendActivity(db.Activity{NodeID: &intent, Worker: "test", Kind: "tool_result", Tool: "Bash", ToolUseID: "real-bash", Detail: result.Flatten(), Metadata: json.RawMessage(`{"execution_provenance":"runtime","result_disposition":"actual","output_scope":"saved_tool_result"}`)})
	if err != nil {
		t.Fatal(err)
	}
	obs := evidenceNode(t, evidenceCall(t, ts.recordObservation(), map[string]any{"summary": "local result", "context_key": "local/test/output/check", "source_kind": "activity", "source_id": fmt.Sprint(act)}, false))
	var observation db.ObservationPayload
	if err = json.Unmarshal(obs.Payload, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.CaptureStatus != "captured" || !strings.Contains(string(observation.SourceSnapshot), "local evidence result") {
		t.Fatal(string(obs.Payload))
	}
	claim := map[string]any{"summary": "output contains local evidence result", "detail": "saved output only", "context_key": "local/test/output/check", "conclusion_kind": "observation", "observation_ids": []int64{obs.ID}}
	audit := evidenceNode(t, evidenceCall(t, ts.auditFactValidity(), claim, false))
	var qual db.QualificationPayload
	if err = json.Unmarshal(audit.Payload, &qual); err != nil {
		t.Fatal(err)
	}
	if qual.Qualification != "ready_for_review" || qual.SemanticStatus != "unknown" {
		t.Fatal(string(audit.Payload))
	}
	claim["audit_id"] = audit.ID
	claim["summary"] = "a different claim"
	evidenceCall(t, ts.recordFact(), claim, true)
	claim["summary"] = "output contains local evidence result"
	claim["detail"] = "a different detail"
	evidenceCall(t, ts.recordFact(), claim, true)
	claim["detail"] = "saved output only"
	claim["context_key"] = "different identity/object/context"
	evidenceCall(t, ts.recordFact(), claim, true)
	claim["context_key"] = "local/test/output/check"
	if _, err = d.Exec(`UPDATE exploration_nodes SET content_version=content_version+1 WHERE id=$1`, obs.ID); err != nil {
		t.Fatal(err)
	}
	evidenceCall(t, ts.recordFact(), claim, true)
	delete(claim, "audit_id")
	currentAudit := evidenceNode(t, evidenceCall(t, ts.auditFactValidity(), claim, false))
	claim["audit_id"] = currentAudit.ID
	if got := evidenceCall(t, ts.recordFact(), claim, false); !strings.HasPrefix(got, "fact recorded:") {
		t.Fatal(got)
	}
	evidenceCall(t, ts.recordFact(), claim, true) // single-use audit
	claim["conclusion_kind"] = "inconclusive"
	delete(claim, "audit_id")
	unknown := evidenceNode(t, evidenceCall(t, ts.auditFactValidity(), claim, false))
	if unknown.State != "unknown" {
		t.Fatal(string(unknown.Payload))
	}
	claim["conclusion_kind"] = "negative"
	negative := evidenceNode(t, evidenceCall(t, ts.auditFactValidity(), claim, false))
	claim["audit_id"] = negative.ID
	evidenceCall(t, ts.recordFact(), claim, true)
	// Source time remains the actual activity timestamp, even when registering
	// that source again; an old execution cannot acquire a new freshness window.
	if _, err = d.Exec(`UPDATE activity SET created_at=now()-interval '2 days' WHERE id=$1`, act); err != nil {
		t.Fatal(err)
	}
	oldObs := evidenceNode(t, evidenceCall(t, ts.recordObservation(), map[string]any{"summary": "old source", "context_key": "local/test/output/check", "source_kind": "activity", "source_id": fmt.Sprint(act)}, false))
	claim["observation_ids"] = []int64{oldObs.ID}
	claim["conclusion_kind"] = "observation"
	delete(claim, "audit_id")
	stale := evidenceNode(t, evidenceCall(t, ts.auditFactValidity(), claim, false))
	if stale.State != "insufficient" {
		t.Fatal(string(stale.Payload))
	}
	claim["audit_id"] = stale.ID
	evidenceCall(t, ts.recordFact(), claim, true)
	model := evidenceNode(t, evidenceCall(t, ts.recordObservation(), map[string]any{"summary": "model text", "context_key": "local/test/output/check", "captured": true}, false))
	var modelPayload db.ObservationPayload
	if err := json.Unmarshal(model.Payload, &modelPayload); err != nil {
		t.Fatal(err)
	}
	if modelPayload.CaptureStatus != "model_recorded" {
		t.Fatal(string(model.Payload))
	}
	claim["observation_ids"] = []int64{model.ID}
	delete(claim, "audit_id")
	modelAudit := evidenceNode(t, evidenceCall(t, ts.auditFactValidity(), claim, false))
	if modelAudit.State != "insufficient" {
		t.Fatal(string(modelAudit.Payload))
	}
	claim["audit_id"] = modelAudit.ID
	evidenceCall(t, ts.recordFact(), claim, true)
	claim["conclusion_kind"] = "inconclusive"
	delete(claim, "audit_id")
	inconclusive := evidenceNode(t, evidenceCall(t, ts.auditFactValidity(), claim, false))
	claim["audit_id"] = inconclusive.ID
	evidenceCall(t, ts.recordFact(), claim, false)
	other, err := d.CreateTask("other evidence fixture", "local", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(other.ID)
	otherIntent, err := d.Exploration(other.ExplorationID).AddIntent(map[string]any{"summary": "other"}, 1, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	evidenceCall(t, ts.recordObservation(), map[string]any{"intent_id": otherIntent, "summary": "wrong task", "context_key": "local/test/output/check"}, true)
	foreign := NewToolSet(d.Exploration(other.ExplorationID), "other")
	foreign.SetTaskID(other.ID)
	foreign.SetOwnerNode(otherIntent)
	evidenceCall(t, foreign.auditFactValidity(), claim, true)
	// Synthetic receipts are visible activity but cannot claim execution.
	if _, err = d.Exec(`UPDATE activity SET metadata='{"execution_provenance":"runtime","result_disposition":"not_invoked"}' WHERE id=$1`, act); err != nil {
		t.Fatal(err)
	}
	evidenceCall(t, ts.recordObservation(), map[string]any{"summary": "receipt", "context_key": "local/test/output/check", "source_kind": "activity", "source_id": fmt.Sprint(act)}, true)
	legacy := evidenceCall(t, ts.recordFact(), map[string]any{"summary": "legacy compatibility"}, false)
	if !strings.HasPrefix(legacy, "fact recorded:") {
		t.Fatal(legacy)
	}
	evidenceCall(t, ts.recordFact(), map[string]any{"intent_id": "bad", "facts": []map[string]any{{"summary": "invalid batch default"}}}, true)
}

func TestEvidenceColdRoundsAndExpandRealPostgres(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("cold evidence", "local", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	es := d.Exploration(task.ExplorationID)
	intent, err := es.AddIntent(map[string]any{"summary": "cold"}, 1, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	ts := NewToolSet(es, "test")
	ts.SetTaskID(task.ID)
	ts.SetOwnerNode(intent)
	obs := evidenceNode(t, evidenceCall(t, ts.recordObservation(), map[string]any{"summary": "preserve model provenance", "context_key": "local/cold"}, false))
	audit := evidenceNode(t, evidenceCall(t, ts.auditFactValidity(), map[string]any{"summary": "limited result", "context_key": "local/cold", "conclusion_kind": "inconclusive", "observation_ids": []int64{obs.ID}}, false))
	compactor := &Compactor{params: defaultColdParams()}
	if _, _, _, err = compactor.maintain(es); err != nil {
		t.Fatal(err)
	}
	stamps, err := es.ColdStamps()
	if err != nil {
		t.Fatal(err)
	}
	if stamps[obs.ID] != nil || stamps[audit.ID] != nil {
		t.Fatal("live intent children folded")
	}
	if err = es.SetIntentState(intent, "done"); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 7; round++ {
		if _, _, _, err = compactor.maintain(es); err != nil {
			t.Fatal(err)
		}
	}
	g, byID, err := loadColdGraph(es)
	if err != nil {
		t.Fatal(err)
	}
	stamps, err = es.ColdStamps()
	if err != nil {
		t.Fatal(err)
	}
	round, err := es.RoundNo()
	if err != nil {
		t.Fatal(err)
	}
	eligible := g.eligibleCold(g.structuralCold(g.hotSet()), stamps, round, defaultColdParams())
	if !eligible[obs.ID] || !eligible[audit.ID] {
		t.Fatalf("missing persisted evidence stamps: %v", stamps)
	}
	blocks := g.group(eligible, defaultColdParams())
	var folded int64
	for _, b := range blocks {
		contains := false
		for _, id := range b.Members {
			if id == obs.ID {
				contains = true
			}
		}
		if !contains {
			continue
		}
		payload := digestPayload("saved evidence chain", b, byID, 1, "real fixture")
		folded, err = es.AddDigest(payload, b.Members)
		if err != nil {
			t.Fatal(err)
		}
	}
	if folded == 0 {
		t.Fatal("evidence chain did not form a cold block")
	}
	expanded := evidenceCall(t, ts.expandDigest(), map[string]any{"id": folded}, false)
	if !strings.Contains(expanded, "observation") || !strings.Contains(expanded, "validity_gate") {
		t.Fatal(expanded)
	}
	detail, err := es.EvidenceDetail(obs.ID)
	if err != nil || detail.Node == nil || len(detail.Edges) == 0 {
		t.Fatalf("lost original evidence %+v %v", detail, err)
	}
}
