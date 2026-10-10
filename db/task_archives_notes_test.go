package db

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func openNotesArchiveFixture(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN fixture")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for _, ensure := range []func() error{d.EnsureLLMRecordsTable, d.EnsureLLMUsageTable} {
		if err := ensure(); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func coldNotesArchiveFixture(t *testing.T, d *DB) (*TaskArchive, *TaskArchiveSnapshot, int64, int64, time.Time) {
	t.Helper()
	task, err := d.CreateTask("private note archive", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID)
		_ = d.DeleteTask(task.ID)
	})
	finding, err := d.Exploration(task.ExplorationID).RecordFinding(t.Context(), RecordFindingInput{TaskID: task.ID, ExplorationID: task.ExplorationID, Summary: "reviewed finding", Severity: "low"})
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 10, 4, 3, 2, 123456000, time.UTC)
	if _, err := d.Exec(`UPDATE findings SET status='resolved',reviewed_by='复核人员',reviewed_at=$2,reviewed_status='resolved' WHERE id=$1`, finding.FindingID, when); err != nil {
		t.Fatal(err)
	}
	var noteID int64
	// An imported explicit ID exercises sequence repair without rewinding a live sequence.
	if err := d.QueryRow(`INSERT INTO finding_notes(id,finding_id,author,body,created_at)
SELECT COALESCE(max(id),0)+100,$1,'内部作者',$2,$3 FROM finding_notes RETURNING id`, finding.FindingID, "私密备注：已复核。\n保留 UTF-8 ✅", when).Scan(&noteID); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET pause_origin='schedule' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job, err := d.ClaimTaskArchiveJob(t.Context()); err != nil || job == nil || job.ID != archive.ID {
		t.Fatalf("claim archive=%+v err=%v", job, err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DataCounts["finding_notes"] != 1 {
		t.Fatalf("note snapshot count=%d", snapshot.DataCounts["finding_notes"])
	}
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "/tmp/notes-fixture", "fixture", 1, 1); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM finding_notes WHERE finding_id=$1`, finding.FindingID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cold notes=%d err=%v", count, err)
	}
	var paused bool
	var origin string
	if err := d.QueryRow(`SELECT paused,pause_origin FROM tasks WHERE id=$1`, task.ID).Scan(&paused, &origin); err != nil || !paused || origin != "manual" {
		t.Fatalf("cold task paused=%v origin=%q err=%v", paused, origin, err)
	}
	if _, err := d.QueueTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	if job, err := d.ClaimTaskArchiveJob(t.Context()); err != nil || job == nil || job.ID != archive.ID {
		t.Fatalf("claim restore=%+v err=%v", job, err)
	}
	return archive, snapshot, finding.FindingID, noteID, when
}

func TestTaskArchiveNotesReviewRoundTrip(t *testing.T) {
	d := openNotesArchiveFixture(t)
	archive, snapshot, findingID, noteID, when := coldNotesArchiveFixture(t, d)
	originalNotes, originalFindings := snapshot.Tables["finding_notes"], snapshot.Tables["findings"]
	for _, corruption := range []string{"foreign_note_link", "duplicate_note_id", "foreign_finding_task"} {
		t.Run(corruption, func(t *testing.T) {
			notes, _ := decodeArchiveRows(originalNotes)
			findings, _ := decodeArchiveRows(originalFindings)
			switch corruption {
			case "foreign_note_link":
				notes[0]["finding_id"] = findingID + 999999
			case "duplicate_note_id":
				notes = append(notes, notes[0])
			case "foreign_finding_task":
				findings[0]["task_id"] = nil
			}
			snapshot.Tables["finding_notes"], _ = json.Marshal(notes)
			snapshot.Tables["findings"], _ = json.Marshal(findings)
			if _, err := d.RestoreTaskArchive(archive.ID, snapshot, 0); err == nil {
				t.Fatal("corrupt ownership accepted")
			}
			var count int
			if err := d.QueryRow(`SELECT count(*) FROM findings WHERE id=$1`, findingID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed restore leaked findings=%d err=%v", count, err)
			}
		})
	}
	snapshot.Tables["finding_notes"], snapshot.Tables["findings"] = originalNotes, originalFindings
	if _, err := d.RestoreTaskArchive(archive.ID, snapshot, 0); err != nil {
		t.Fatal(err)
	}
	var gotID, gotFinding int64
	var author, body string
	var createdAt time.Time
	if err := d.QueryRow(`SELECT id,finding_id,author,body,created_at FROM finding_notes WHERE id=$1`, noteID).Scan(&gotID, &gotFinding, &author, &body, &createdAt); err != nil {
		t.Fatal(err)
	}
	if gotID != noteID || gotFinding != findingID || author != "内部作者" || body != "私密备注：已复核。\n保留 UTF-8 ✅" || !createdAt.Equal(when) {
		t.Fatalf("restored note changed: id=%d finding=%d author=%q body=%q time=%s", gotID, gotFinding, author, body, createdAt)
	}
	var reviewer, reviewedStatus string
	var reviewedAt time.Time
	if err := d.QueryRow(`SELECT reviewed_by,reviewed_at,reviewed_status FROM findings WHERE id=$1`, findingID).Scan(&reviewer, &reviewedAt, &reviewedStatus); err != nil || reviewer != "复核人员" || reviewedStatus != "resolved" || !reviewedAt.Equal(when) {
		t.Fatalf("review=%q %s %q err=%v", reviewer, reviewedAt, reviewedStatus, err)
	}
	var paused, queued bool
	var origin string
	if err := d.QueryRow(`SELECT paused,queued,pause_origin FROM tasks WHERE id=$1`, archive.TaskID).Scan(&paused, &queued, &origin); err != nil || !paused || queued || origin != "manual" {
		t.Fatalf("restored task paused=%v queued=%v origin=%q err=%v", paused, queued, origin, err)
	}
	var nextID int64
	if err := d.QueryRow(`INSERT INTO finding_notes(finding_id,author,body) VALUES($1,'next','next') RETURNING id`, findingID).Scan(&nextID); err != nil || nextID <= noteID {
		t.Fatalf("next note=%d archived=%d err=%v", nextID, noteID, err)
	}
	if err := d.CompleteTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTaskArchiveNotesLegacyDefaults(t *testing.T) {
	d := openNotesArchiveFixture(t)
	for _, version := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			archive, snapshot, findingID, _, _ := coldNotesArchiveFixture(t, d)
			snapshot.FormatVersion = version
			delete(snapshot.Tables, "finding_notes")
			delete(snapshot.DataCounts, "finding_notes")
			for _, table := range []string{"traffic_evidence_snapshots", "finding_traffic_bindings"} {
				delete(snapshot.Tables, table)
			}
			findings, _ := decodeArchiveRows(snapshot.Tables["findings"])
			for _, row := range findings {
				for _, field := range []string{"reviewed_by", "reviewed_at", "reviewed_status"} {
					delete(row, field)
				}
			}
			snapshot.Tables["findings"], _ = json.Marshal(findings)
			tasks, _ := decodeArchiveRows(snapshot.Tables["tasks"])
			delete(tasks[0], "pause_origin")
			snapshot.Tables["tasks"], _ = json.Marshal(tasks)
			if _, err := d.RestoreTaskArchive(archive.ID, snapshot, 0); err != nil {
				t.Fatal(err)
			}
			var valid bool
			if err := d.QueryRow(`SELECT reviewed_by='' AND reviewed_status='' AND reviewed_at IS NULL FROM findings WHERE id=$1`, findingID).Scan(&valid); err != nil || !valid {
				t.Fatalf("legacy review default=%v err=%v", valid, err)
			}
			var origin string
			if err := d.QueryRow(`SELECT pause_origin FROM tasks WHERE id=$1`, archive.TaskID).Scan(&origin); err != nil || origin != "manual" {
				t.Fatalf("legacy origin=%q err=%v", origin, err)
			}
			if err := d.CompleteTaskArchiveRestore(archive.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
