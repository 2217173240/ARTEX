package db

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestTaskArchiveCaseRoundTrip(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN fixture")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, ensure := range []func() error{d.EnsureLLMRecordsTable, d.EnsureLLMUsageTable} {
		if err := ensure(); err != nil {
			t.Fatal(err)
		}
	}
	task, err := d.CreateTask("case archive fixture", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := d.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var ids []int64
	for _, summary := range []string{"first", "second", "third"} {
		f, err := d.Exploration(task.ExplorationID).RecordFinding(t.Context(), RecordFindingInput{TaskID: task.ID, ExplorationID: task.ExplorationID, Summary: summary, Severity: "low"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.FindingID)
	}
	// A real empty-body evidence fixture has deterministic hashes and snapshot ID.
	bodyHash := fmt.Sprintf("%x", sha256.Sum256(nil))
	evidence := TrafficEvidenceSnapshot{SourceTrafficID: "case-archive-fixture", URL: "https://fixture.invalid/", Method: "GET", Status: 200, ReqHash: bodyHash, RespHash: bodyHash}
	evidence.ID = TrafficSnapshotID(evidence)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := InsertEvidenceSnapshotTx(tx, evidence); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	must(`INSERT INTO finding_traffic_bindings(finding_id,snapshot_id,role,note,position) VALUES($1,$2,'proof','fixture evidence',0)`, ids[0], evidence.ID)
	must(`UPDATE findings SET evidence_version=7,report_evidence_version=6,report='finding report' WHERE task_id=$1`, task.ID)
	var cid int64
	if err := d.QueryRow(`INSERT INTO finding_cases(task_id,origin_task_id,title,reason,report,version,report_version) VALUES($1,$1,'group','same defect','case report',9,8) RETURNING id`, task.ID).Scan(&cid); err != nil {
		t.Fatal(err)
	}
	must(`INSERT INTO finding_case_members(finding_id,case_id) VALUES($1,$3),($2,$3)`, ids[0], ids[1], cid)
	must(`INSERT INTO finding_case_events(case_id,action,actor,reason,finding_ids) VALUES($1,'merge','human','verified',jsonb_build_array($2::bigint,$3::bigint))`, cid, ids[0], ids[1])
	must(`INSERT INTO finding_case_events(case_id,action,actor,reason,finding_ids) VALUES($1,'report_saved','human','report recorded','[]')`, cid)
	must(`INSERT INTO finding_case_suggestions(task_id,left_id,right_id,title,reason) VALUES($1,$2,$3,'candidate','review')`, task.ID, ids[0], ids[2])
	must(`INSERT INTO finding_case_blocks(left_id,right_id,reason) VALUES($1,$2,'different')`, ids[1], ids[2])
	var conversationID int64
	if err := d.QueryRow(`INSERT INTO conversations(agent_key,title) VALUES('fixture','case review') RETURNING id`).Scan(&conversationID); err != nil {
		t.Fatal(err)
	}
	defer must(`DELETE FROM conversations WHERE id=$1`, conversationID)
	must(`INSERT INTO finding_case_review_runs(conversation_id,task_id,state,error,finding_ids,reviewed_ids,conclusion) VALUES($1,$2,'completed','',jsonb_build_array($3::bigint),jsonb_build_array($3::bigint),'{"summary":"archive conclusion"}'::jsonb)`, conversationID, task.ID, ids[0])
	if err = d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.CompleteTaskArchive(archive.ID, snapshot, "/tmp/case-fixture", "fixture", 1, 1); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = d.QueryRow(`SELECT count(*) FROM finding_cases WHERE task_id=$1`, task.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cold cases=%d err=%v", count, err)
	}
	// Occupy the previous case ID with unrelated metadata; recovery must remap.
	must(`INSERT INTO finding_cases(id,origin_task_id,title,reason) VALUES($1,999999,'unrelated','collision')`, cid)
	var events []map[string]any
	if err := json.Unmarshal(snapshot.Tables["finding_case_events"], &events); err != nil {
		t.Fatal(err)
	}
	occupiedEventID := events[0]["id"]
	must(`INSERT INTO finding_case_events(id,case_id,action,actor,reason) VALUES($1,$2,'unrelated','fixture','collision')`, occupiedEventID, cid)
	defer func() {
		must(`DELETE FROM finding_case_events WHERE case_id=$1`, cid)
		must(`DELETE FROM finding_cases WHERE id=$1`, cid)
	}()

	if _, err = d.QueueTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A corrupt cross-task pair must roll back the entire restore.
	original := snapshot.Tables["finding_case_blocks"]
	snapshot.Tables["finding_case_blocks"] = json.RawMessage(`[{"left_id":999999999,"right_id":9999999999,"reason":"invalid"}]`)
	if _, err = d.RestoreTaskArchive(archive.ID, snapshot, 0); err == nil {
		t.Fatal("invalid finding reference accepted")
	}
	if err = d.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, task.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed restore leaked findings=%d err=%v", count, err)
	}
	snapshot.Tables["finding_case_blocks"] = original
	if _, err = d.RestoreTaskArchive(archive.ID, snapshot, 0); err != nil {
		t.Fatal(err)
	}
	var restoredID, version, reportVersion int64
	var report string
	if err = d.QueryRow(`SELECT id,version,report_version,report FROM finding_cases WHERE task_id=$1`, task.ID).Scan(&restoredID, &version, &reportVersion, &report); err != nil {
		t.Fatal(err)
	}
	if restoredID == cid || version != 9 || reportVersion != 8 || report != "case report" {
		t.Fatalf("case=%d version=%d reportVersion=%d report=%s", restoredID, version, reportVersion, report)
	}
	var latestAction string
	if err := d.QueryRow(`SELECT action FROM finding_case_events WHERE case_id=$1 ORDER BY id DESC LIMIT 1`, restoredID).Scan(&latestAction); err != nil || latestAction != "report_saved" {
		t.Fatalf("history order=%s err=%v", latestAction, err)
	}
	for table, want := range map[string]int{"finding_case_members": 2, "finding_case_events": 2} {
		if err = d.QueryRow(`SELECT count(*) FROM `+table+` WHERE case_id=$1`, restoredID).Scan(&count); err != nil || count != want {
			t.Fatalf("%s=%d err=%v", table, count, err)
		}
	}
	if err = d.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1 AND evidence_version=7 AND report_evidence_version=6 AND report='finding report'`, task.ID).Scan(&count); err != nil || count != 3 {
		t.Fatalf("finding versions=%d err=%v", count, err)
	}
	if err = d.QueryRow(`SELECT count(*) FROM finding_case_suggestions WHERE task_id=$1`, task.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("suggestions=%d err=%v", count, err)
	}
	if err = d.QueryRow(`SELECT count(*) FROM finding_case_blocks WHERE left_id=$1 AND right_id=$2`, ids[1], ids[2]).Scan(&count); err != nil || count != 1 {
		t.Fatalf("blocks=%d err=%v", count, err)
	}
	if err = d.QueryRow(`SELECT count(*) FROM finding_case_review_runs WHERE conversation_id=$1 AND task_id=$2 AND state='completed' AND finding_ids=jsonb_build_array($3::bigint) AND reviewed_ids=jsonb_build_array($3::bigint) AND conclusion->>'summary'='archive conclusion'`, conversationID, task.ID, ids[0]).Scan(&count); err != nil || count != 1 {
		t.Fatalf("review runs=%d err=%v", count, err)
	}
	traffic, err := d.GetFindingTraffic(t.Context(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic.Bindings) != 1 || traffic.Bindings[0].Snapshot.ReqHash != bodyHash || traffic.Bindings[0].Snapshot.RespHash != bodyHash || traffic.Bindings[0].Role != "proof" || traffic.Bindings[0].Note != "fixture evidence" {
		t.Fatalf("evidence roundtrip: %+v", traffic)
	}
	must(`DELETE FROM finding_case_members WHERE case_id=$1`, restoredID)
	must(`DELETE FROM finding_case_events WHERE case_id=$1`, restoredID)
	must(`DELETE FROM finding_cases WHERE id=$1`, restoredID)
	must(`DELETE FROM task_archives WHERE id=$1`, archive.ID)
	if err = d.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
}
