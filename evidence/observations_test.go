package evidence

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestObservationPinsUnboundSnapshotThroughGCAndArchive(t *testing.T) {
	s, in, dir := evidenceFixture(t)
	ctx := t.Context()
	if err := s.DB.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	body := []byte("persistent local traffic fixture response")
	seedExchange(t, s, "observation-body", body, false)
	recorded, err := s.Record(ctx, in, []db.TrafficRef{{TrafficID: "observation-body"}})
	if err != nil {
		t.Fatal(err)
	}
	binding := recorded.Traffic.Bindings[0]
	es := s.DB.Exploration(in.ExplorationID)
	intent, err := es.AddIntent(map[string]any{"summary": "local evidence"}, 1, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	obs, err := es.RecordObservation(ctx, db.ObservationInput{TaskID: in.TaskID, IntentID: intent, Summary: "saved response", SourceKind: "traffic_snapshot", SourceID: binding.SnapshotID, ContextKey: "fixture.local/anonymous/response/compare", Worker: "test"})
	if err != nil {
		t.Fatal(err)
	}
	var p db.ObservationPayload
	if err = json.Unmarshal(obs.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if !p.SourceTime.Equal(time.Unix(binding.Snapshot.CapturedAt, 0)) {
		t.Fatalf("source clock changed %v %d", p.SourceTime, binding.Snapshot.CapturedAt)
	}
	claim := db.QualificationInput{TaskID: in.TaskID, IntentID: intent, Summary: "saved local response", ConclusionKind: "observation", ContextKey: p.ContextKey, ObservationIDs: []int64{obs.ID}}
	audit, err := es.AuditFactValidity(ctx, claim)
	if err != nil || audit.State != "ready_for_review" {
		t.Fatalf("snapshot qualification %+v %v", audit, err)
	}
	if err = s.DB.EditFindingTraffic(ctx, recorded.FindingID, binding.ID, recorded.Traffic.Version, nil, nil, true, nil); err != nil {
		t.Fatal(err)
	}
	for _, now := range []time.Time{time.Now().Add(48 * time.Hour), time.Now().Add(96 * time.Hour)} {
		if err = s.Collect(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	readBody := func() {
		t.Helper()
		f, _, err := s.OpenBody(binding.Snapshot, "response")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		got, err := io.ReadAll(f)
		if err != nil || string(got) != string(body) {
			t.Fatalf("body changed %q %v", got, err)
		}
	}
	resolved, err := s.ObservationSnapshot(ctx, in.TaskID, obs.ID)
	if err != nil || resolved.ID != binding.SnapshotID {
		t.Fatalf("observation body resolution %+v %v", resolved, err)
	}
	other, err := s.DB.CreateTask("foreign body scope", "local", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.DeleteTask(other.ID)
	if _, err = s.ObservationSnapshot(ctx, other.ID, obs.ID); err != sql.ErrNoRows {
		t.Fatalf("foreign task read result %v", err)
	}
	if _, err = s.ObservationSnapshot(ctx, in.TaskID, recorded.NodeID); err != sql.ErrNoRows {
		t.Fatalf("non-observation read result %v", err)
	}
	readBody()
	if _, err = s.DB.Exec(`UPDATE tasks SET paused=true WHERE id=$1`, in.TaskID); err != nil {
		t.Fatal(err)
	}
	archive, err := s.DB.QueueTaskArchive(in.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.DB.ClaimTaskArchiveJob(ctx)
	if err != nil || job == nil || job.ID != archive.ID {
		t.Fatalf("archive job %+v %v", job, err)
	}
	snapshot, err := s.DB.SnapshotTaskArchive(in.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := db.ArchiveEvidenceSnapshots(snapshot)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("pure observation snapshot missing %v %v", snapshots, err)
	}
	archiveDir := filepath.Join(dir, "observation-archive")
	if err = s.CopySnapshots(ctx, snapshots, archiveDir); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(archiveDir, "database-snapshot.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if err = s.DB.CompleteTaskArchive(archive.ID, snapshot, path, hex.EncodeToString(sum[:]), int64(len(raw)), int64(len(raw))); err != nil {
		t.Fatal(err)
	}
	for _, now := range []time.Time{time.Now().Add(48 * time.Hour), time.Now().Add(96 * time.Hour)} {
		if err = s.Collect(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.DB.QueueTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	job, err = s.DB.ClaimTaskArchiveJob(ctx)
	if err != nil || job == nil || job.State != db.Restoring {
		t.Fatalf("restore job %+v %v", job, err)
	}
	var tampered db.TaskArchiveSnapshot
	if err = json.Unmarshal(raw, &tampered); err != nil {
		t.Fatal(err)
	}
	var nodes []map[string]any
	if err = json.Unmarshal(tampered.Tables["exploration_nodes"], &nodes); err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node["kind"] == db.KindObservation {
			node["payload"].(map[string]any)["source_id"] = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		}
	}
	tampered.Tables["exploration_nodes"], err = json.Marshal(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.WithInstalledSnapshots(ctx, snapshots, archiveDir, func() error { _, e := s.DB.RestoreTaskArchive(archive.ID, &tampered, 0); return e }); err == nil {
		t.Fatal("restored observation with snapshot outside package")
	}
	var restoredNodes int
	if err = s.DB.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, in.ExplorationID).Scan(&restoredNodes); err != nil || restoredNodes != 0 {
		t.Fatalf("partial bad restore count=%d err=%v", restoredNodes, err)
	}
	if err = s.WithInstalledSnapshots(ctx, snapshots, archiveDir, func() error { _, err := s.DB.RestoreTaskArchive(archive.ID, snapshot, 0); return err }); err != nil {
		t.Fatal(err)
	}
	readBody()
	if err = s.Collect(ctx, time.Now().Add(120*time.Hour)); err != nil {
		t.Fatal(err)
	}
	readBody()
	got, err := es.GetNode(obs.ID)
	if err != nil || got == nil || string(got.Payload) != string(obs.Payload) {
		t.Fatalf("observation changed %+v %v", got, err)
	}
}
