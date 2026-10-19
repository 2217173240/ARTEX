package db

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func archiveRetestFixture(t *testing.T, d *DB) (*Task, *TaskArchive, *TaskArchiveSnapshot, *FindingRetest, *Conversation) {
	t.Helper()
	task := outcomeTestTask(t, d)
	fid, err := d.AddFinding(task.ID, 0, "fixture", "retest archive", "high", "summary", "original evidence", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	r, c, created, err := d.CreateFindingRetest(t.Context(), fid, "operator notes")
	if err != nil || !created {
		t.Fatalf("create retest: %+v %v", r, err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM conversations WHERE id=$1`, c.ID) })
	if started, err := d.StartFindingRetest(t.Context(), r.ID); err != nil || !started {
		t.Fatalf("start: %v %v", started, err)
	}
	if err := d.RecordFindingRetestResult(t.Context(), c.ID, "reproduced", "completed summary", "immutable retest evidence"); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishFindingRetest(r.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO conversation_activities(conversation_id,worker,kind,summary,detail) VALUES($1,'retester','assistant','retest output','actual retained conversation output')`, c.ID); err != nil {
		t.Fatal(err)
	}
	r, err = d.FindingRetestForConversation(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	a, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := d.ClaimTaskArchiveJob(t.Context())
	if err != nil || job == nil || job.ID != a.ID {
		t.Fatalf("claim: %+v %v", job, err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FormatVersion != 4 || snapshot.DataCounts["finding_retests"] != 1 {
		t.Fatalf("retest manifest: %+v", snapshot.DataCounts)
	}
	if err := d.CompleteTaskArchive(a.ID, snapshot, "unused-retest-fixture-package", "fixture", 1, 1); err != nil {
		t.Fatal(err)
	}
	return task, a, snapshot, r, c
}

func claimRetestArchiveRestore(t *testing.T, d *DB, a *TaskArchive) {
	t.Helper()
	if _, err := d.QueueTaskArchiveRestore(a.ID); err != nil {
		t.Fatal(err)
	}
	job, err := d.ClaimTaskArchiveJob(t.Context())
	if err != nil || job == nil || job.ID != a.ID || job.State != Restoring {
		t.Fatalf("restore claim: %+v %v", job, err)
	}
}

func TestTaskArchiveRetestsRoundTrip(t *testing.T) {
	d := outcomeTestDB(t)
	task, a, snapshot, before, c := archiveRetestFixture(t, d)
	cold, err := d.TaskOutcome(t.Context(), task.ID)
	if err != nil || cold.Retests.Total != 1 || cold.Retests.Reproduced != 1 || cold.Partial {
		t.Fatalf("cold outcome: %+v %v", cold, err)
	}
	var rows, conversations, outputs int
	if err := d.QueryRow(`SELECT count(*) FROM finding_retests WHERE id=$1`, before.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("cold retest rows=%d %v", rows, err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM conversations WHERE id=$1`, c.ID).Scan(&conversations); err != nil || conversations != 1 {
		t.Fatalf("retained conversations=%d %v", conversations, err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM conversation_activities WHERE conversation_id=$1 AND detail='actual retained conversation output'`, c.ID).Scan(&outputs); err != nil || outputs != 1 {
		t.Fatalf("retained outputs=%d %v", outputs, err)
	}
	claimRetestArchiveRestore(t, d, a)
	warnings, err := d.RestoreTaskArchive(a.ID, snapshot, 0)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("restore: %v %v", warnings, err)
	}
	if err := d.CompleteTaskArchiveRestore(a.ID); err != nil {
		t.Fatal(err)
	}
	after, err := d.FindingRetestForConversation(t.Context(), c.ID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("restored history=%+v want %+v (%v)", after, before, err)
	}
	live, err := d.TaskOutcome(t.Context(), task.ID)
	cold.Source = "live"
	if err != nil || !reflect.DeepEqual(live, cold) {
		t.Fatalf("restored outcome=%+v want %+v (%v)", live, cold, err)
	}
	history, err := d.ListFindingRetests(before.FindingID)
	if err != nil || len(history) != 1 || history[0].Summary != before.Summary || history[0].Evidence != before.Evidence || history[0].ConversationID == nil || *history[0].ConversationID != c.ID {
		t.Fatalf("history=%+v %v", history, err)
	}
}

func TestTaskArchiveRetestsMissingConversation(t *testing.T) {
	d := outcomeTestDB(t)
	task, a, snapshot, before, c := archiveRetestFixture(t, d)
	if _, err := d.Exec(`DELETE FROM conversations WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	claimRetestArchiveRestore(t, d, a)
	warnings, err := d.RestoreTaskArchive(a.ID, snapshot, 0)
	if err != nil || len(warnings) != 1 {
		t.Fatalf("restore: %v %v", warnings, err)
	}
	if err := d.CompleteTaskArchiveRestore(a.ID); err != nil {
		t.Fatal(err)
	}
	history, err := d.ListFindingRetests(before.FindingID)
	if err != nil || len(history) != 1 || history[0].ConversationID != nil || history[0].Evidence != before.Evidence {
		t.Fatalf("history=%+v %v", history, err)
	}
	out, err := d.TaskOutcome(t.Context(), task.ID)
	if err != nil || !out.Partial || !reflect.DeepEqual(out.Unavailable, []string{"retests"}) {
		t.Fatalf("partial=%+v %v", out, err)
	}
}

func TestTaskArchiveRetestsLegacyMissingHistory(t *testing.T) {
	d := outcomeTestDB(t)
	for _, version := range []int{1, 2, 3} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			task, a, snapshot, before, _ := archiveRetestFixture(t, d)
			snapshot.FormatVersion = version
			delete(snapshot.Tables, "finding_retests")
			delete(snapshot.DataCounts, "finding_retests")
			rows, err := decodeArchiveRows(snapshot.Tables["tasks"])
			if err != nil {
				t.Fatal(err)
			}
			delete(rows[0], "retest_history_complete")
			snapshot.Tables["tasks"], err = json.Marshal(rows)
			if err != nil {
				t.Fatal(err)
			}
			claimRetestArchiveRestore(t, d, a)
			warnings, err := d.RestoreTaskArchive(a.ID, snapshot, 0)
			if err != nil || len(warnings) != 1 {
				t.Fatalf("legacy restore: %v %v", warnings, err)
			}
			if err := d.CompleteTaskArchiveRestore(a.ID); err != nil {
				t.Fatal(err)
			}
			history, err := d.ListFindingRetests(before.FindingID)
			if err != nil || len(history) != 0 {
				t.Fatalf("legacy history must remain absent: %+v %v", history, err)
			}
			out, err := d.TaskOutcome(t.Context(), task.ID)
			if err != nil || !out.Partial || !reflect.DeepEqual(out.Unavailable, []string{"retests"}) {
				t.Fatalf("legacy outcome=%+v %v", out, err)
			}
			_, conv, created, err := d.CreateFindingRetest(t.Context(), before.FindingID, "new attempt after historical gap")
			if err != nil || !created {
				t.Fatalf("new retest: %v %v", created, err)
			}
			t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM conversations WHERE id=$1`, conv.ID) })
			out, err = d.TaskOutcome(t.Context(), task.ID)
			if err != nil || !out.Partial {
				t.Fatalf("new attempt must preserve historical gap: %+v %v", out, err)
			}
		})
	}
}

func TestTaskArchiveRetestsRejectInvalidAndRollback(t *testing.T) {
	d := outcomeTestDB(t)
	for _, kind := range []string{"missing_table", "active", "foreign_finding", "conversation_conflict", "row_count"} {
		t.Run(kind, func(t *testing.T) {
			task, a, snapshot, _, c := archiveRetestFixture(t, d)
			rows, err := decodeArchiveRows(snapshot.Tables["finding_retests"])
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing_table":
				delete(snapshot.Tables, "finding_retests")
			case "active":
				rows[0]["status"] = "running"
			case "foreign_finding":
				rows[0]["finding_id"] = int64(9223372036854775807)
			case "conversation_conflict":
				if _, err := d.Exec(`UPDATE conversations SET agent_key='other-agent' WHERE id=$1`, c.ID); err != nil {
					t.Fatal(err)
				}
			case "row_count":
				snapshot.DataCounts["finding_retests"] = 2
			}
			if kind != "missing_table" {
				snapshot.Tables["finding_retests"], err = json.Marshal(rows)
				if err != nil {
					t.Fatal(err)
				}
			}
			claimRetestArchiveRestore(t, d, a)
			if _, err := d.RestoreTaskArchive(a.ID, snapshot, 0); err == nil {
				t.Fatal("invalid retest archive must reject")
			}
			var restored int
			if err := d.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, task.ID).Scan(&restored); err != nil || restored != 0 {
				t.Fatalf("restore transaction did not roll back: %d %v", restored, err)
			}
		})
	}
}

func TestTaskArchiveRetestsCreationAndCompactionBarrier(t *testing.T) {
	d := outcomeTestDB(t)
	task := outcomeTestTask(t, d)
	fid, err := d.AddFinding(task.ID, 0, "fixture", "barrier", "low", "summary", "evidence", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	a, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.CreateFindingRetest(t.Context(), fid, "during queued archive"); !errors.Is(err, ErrTaskArchiveState) {
		t.Fatalf("queued archive allowed new retest: %v", err)
	}
	if _, err := d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 真实数据库注入活动行验证最终事务仍会拒绝，不删除来源证据。
	if _, err := d.Exec(`INSERT INTO finding_retests(finding_id,status,snapshot) VALUES($1,'running','{}')`, fid); err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchive(a.ID, snapshot, "unused-barrier-package", "fixture", 1, 1); !errors.Is(err, ErrTaskArchiveRetestActive) {
		t.Fatalf("active compaction error: %v", err)
	}
	var retained int
	if err := d.QueryRow(`SELECT count(*) FROM findings WHERE id=$1`, fid).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("finding lost during rejected compaction: %d %v", retained, err)
	}
}

func TestTaskArchiveRetestsConcurrentQueueAndCreate(t *testing.T) {
	d := outcomeTestDB(t)
	task := outcomeTestTask(t, d)
	fid, err := d.AddFinding(task.ID, 0, "fixture", "concurrent archive", "low", "summary", "evidence", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	barrier, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback()
	if _, err := barrier.Exec(`SELECT id FROM tasks WHERE id=$1 FOR UPDATE`, task.ID); err != nil {
		t.Fatal(err)
	}
	type creation struct {
		conv    *Conversation
		created bool
		err     error
	}
	queueResult := make(chan error, 1)
	createResult := make(chan creation, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() { _, err := d.QueueTaskArchive(task.ID); queueResult <- err }()
	go func() {
		_, conv, created, err := d.CreateFindingRetest(ctx, fid, "concurrent")
		createResult <- creation{conv, created, err}
	}()
	if err := barrier.Commit(); err != nil {
		t.Fatal(err)
	}
	var queueErr error
	var created creation
	select {
	case queueErr = <-queueResult:
	case <-ctx.Done():
		t.Fatal("archive queue did not finish")
	}
	select {
	case created = <-createResult:
	case <-ctx.Done():
		t.Fatal("retest creation did not finish")
	}
	if created.conv != nil {
		t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM conversations WHERE id=$1`, created.conv.ID) })
	}
	if queueErr == nil {
		if !errors.Is(created.err, ErrTaskArchiveState) || created.created {
			t.Fatalf("queued task acquired new retest: %+v", created)
		}
	} else if !errors.Is(queueErr, ErrTaskArchiveRetestActive) || created.err != nil || !created.created {
		t.Fatalf("creation/queue did not serialize: queue=%v create=%+v", queueErr, created)
	}
}

func TestTaskArchiveRetestsRejectCrossArchiveConversation(t *testing.T) {
	d := outcomeTestDB(t)
	taskA, archiveA, snapshotA, _, _ := archiveRetestFixture(t, d)
	_, archiveB, snapshotB, _, convB := archiveRetestFixture(t, d)
	original := append([]byte(nil), snapshotA.Tables["finding_retests"]...)
	rows, err := decodeArchiveRows(original)
	if err != nil {
		t.Fatal(err)
	}
	rows[0]["conversation_id"] = convB.ID
	snapshotA.Tables["finding_retests"], err = json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	claimRetestArchiveRestore(t, d, archiveA)
	if _, err := d.RestoreTaskArchive(archiveA.ID, snapshotA, 0); err == nil {
		t.Fatal("cross-archive conversation must be rejected")
	}
	var restored int
	if err := d.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, taskA.ID).Scan(&restored); err != nil || restored != 0 {
		t.Fatalf("foreign conversation restore did not roll back: %d %v", restored, err)
	}
	snapshotA.Tables["finding_retests"] = original
	if _, err := d.RestoreTaskArchive(archiveA.ID, snapshotA, 0); err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchiveRestore(archiveA.ID); err != nil {
		t.Fatal(err)
	}
	claimRetestArchiveRestore(t, d, archiveB)
	if _, err := d.RestoreTaskArchive(archiveB.ID, snapshotB, 0); err != nil {
		t.Fatalf("rejected package must preserve other archive ownership: %v", err)
	}
	if err := d.CompleteTaskArchiveRestore(archiveB.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTaskArchiveRetestsMissingConversationProvenance(t *testing.T) {
	d := outcomeTestDB(t)
	task, a, snapshot, before, c := archiveRetestFixture(t, d)
	if _, err := d.Exec(`UPDATE conversations SET retest_binding='{}' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	claimRetestArchiveRestore(t, d, a)
	warnings, err := d.RestoreTaskArchive(a.ID, snapshot, 0)
	if err != nil || len(warnings) != 1 {
		t.Fatalf("restore unknown provenance: %v %v", warnings, err)
	}
	if err := d.CompleteTaskArchiveRestore(a.ID); err != nil {
		t.Fatal(err)
	}
	history, err := d.ListFindingRetests(before.FindingID)
	if err != nil || len(history) != 1 || history[0].ConversationID != nil {
		t.Fatalf("unknown conversation provenance should be detached: %+v %v", history, err)
	}
	out, err := d.TaskOutcome(t.Context(), task.ID)
	if err != nil || !out.Partial {
		t.Fatalf("unknown provenance outcome: %+v %v", out, err)
	}
}
