package db

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEvidenceArchiveRestoresOriginalChain(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err = d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err = d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTask("archive evidence", "preserve chain", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	es := d.Exploration(task.ExplorationID)
	intent, err := es.AddIntent(map[string]any{"summary": "saved local observation"}, 1, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	obs, err := es.RecordObservation(t.Context(), ObservationInput{TaskID: task.ID, IntentID: intent, Summary: "model recorded context", Detail: "retained original source", ContextKey: "local/archive", Worker: "test"})
	if err != nil {
		t.Fatal(err)
	}
	claim := QualificationInput{TaskID: task.ID, IntentID: intent, Summary: "inconclusive result", ConclusionKind: "inconclusive", ContextKey: "local/archive", ObservationIDs: []int64{obs.ID}, Worker: "test"}
	audit, err := es.AuditFactValidity(t.Context(), claim)
	if err != nil {
		t.Fatal(err)
	}
	fact, err := es.RecordFactWithEvidence(t.Context(), FactEvidenceInput{QualificationInput: claim, AuditID: audit.ID})
	if err != nil {
		t.Fatal(err)
	}
	original, err := es.EvidenceDetail(fact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE tasks SET paused=true WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := d.ClaimTaskArchiveJob(t.Context())
	if err != nil || job == nil || job.ID != archive.ID {
		t.Fatalf("archive job %+v %v", job, err)
	}
	if _, err = es.RecordObservation(t.Context(), ObservationInput{TaskID: task.ID, IntentID: intent, Summary: "late write", ContextKey: "local/archive"}); err == nil {
		t.Fatal("archive accepted late evidence")
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "database-snapshot.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	if err = d.CompleteTaskArchive(archive.ID, snapshot, path, hex.EncodeToString(hash[:]), int64(len(raw)), int64(len(raw))); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, task.ExplorationID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("hot rows %d %v", count, err)
	}
	if _, err = d.QueueTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	job, err = d.ClaimTaskArchiveJob(t.Context())
	if err != nil || job == nil || job.State != Restoring {
		t.Fatalf("restore job %+v %v", job, err)
	}
	foreign, err := d.CreateTask("foreign evidence restore", "local", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(foreign.ID)
	foreignStore := d.Exploration(foreign.ExplorationID)
	foreignIntent, err := foreignStore.AddIntent(map[string]any{"summary": "foreign"}, 1, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	foreignObservation, err := foreignStore.RecordObservation(t.Context(), ObservationInput{TaskID: foreign.ID, IntentID: foreignIntent, Summary: "foreign source", ContextKey: "local/archive"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []string{"gate_refs", "fact_refs", "fact_audit", "support_edge", "claim_hash"} {
		t.Run(test, func(t *testing.T) {
			var invalid TaskArchiveSnapshot
			if err := json.Unmarshal(raw, &invalid); err != nil {
				t.Fatal(err)
			}
			nodes, err := decodeArchiveRows(invalid.Tables["exploration_nodes"])
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range nodes {
				id, _ := jsonInt64(n["id"])
				payload := n["payload"].(map[string]any)
				switch {
				case test == "gate_refs" && id == audit.ID:
					payload["observation_ids"] = []int64{foreignObservation.ID}
				case test == "fact_refs" && id == fact:
					payload["observation_ids"] = []int64{foreignObservation.ID}
				case test == "fact_audit" && id == fact:
					payload["audit_id"] = foreignObservation.ID
				case test == "claim_hash" && id == fact:
					payload["summary"] = "altered restored claim"
				}
			}
			invalid.Tables["exploration_nodes"], err = json.Marshal(nodes)
			if err != nil {
				t.Fatal(err)
			}
			if test == "support_edge" {
				edges, err := decodeArchiveRows(invalid.Tables["exploration_edges"])
				if err != nil {
					t.Fatal(err)
				}
				edges = append(edges, map[string]any{"exploration_id": task.ExplorationID, "src_id": foreignObservation.ID, "dst_id": fact, "rel": RelSupports})
				invalid.Tables["exploration_edges"], err = json.Marshal(edges)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = d.RestoreTaskArchive(archive.ID, &invalid, 0); err == nil {
				t.Fatal("invalid evidence closure restored")
			}
			var count int
			if err = d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, task.ExplorationID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("bad restore left %d nodes %v", count, err)
			}
		})
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var recovered TaskArchiveSnapshot
	if err = json.Unmarshal(saved, &recovered); err != nil {
		t.Fatal(err)
	}
	if _, err = d.RestoreTaskArchive(archive.ID, &recovered, 0); err != nil {
		t.Fatal(err)
	}
	restored, err := es.EvidenceDetail(fact)
	if err != nil {
		t.Fatal(err)
	}
	if string(original.Node.Payload) != string(restored.Node.Payload) || len(original.Edges) != len(restored.Edges) {
		t.Fatalf("fact chain changed: %+v %+v", original, restored)
	}
	for _, n := range []*Node{obs, audit} {
		got, err := es.GetNode(n.ID)
		if err != nil || got == nil || string(got.Payload) != string(n.Payload) {
			t.Fatalf("lost source/gate %d: %+v %v", n.ID, got, err)
		}
	}
	if _, err = es.RecordFactWithEvidence(t.Context(), FactEvidenceInput{QualificationInput: claim, AuditID: audit.ID}); err == nil {
		t.Fatal("restored audit lost single-use binding")
	}
}
