package db

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func isolatedFindingNotesDB(t *testing.T) *DB {
	t.Helper()
	raw := os.Getenv("ARTEX_NOTES_TEST_ADMIN_DSN")
	if raw == "" {
		t.Skip("ARTEX_NOTES_TEST_ADMIN_DSN required for isolated finding notes integration database")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	name := "artex_r9_notes_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`); admin.Close() })
	u.Path = "/" + name
	d, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestFindingNotesValidationPaginationAndContext(t *testing.T) {
	d := isolatedFindingNotesDB(t)
	ctx := context.Background()
	task, err := d.CreateTask("notes source", "local fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	fid, err := d.AddFinding(task.ID, 0, "TEST", "notes", "high", "summary", "immutable", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	child, err := d.CreateTaskWithOptions("child", "fixture", TaskCreateOptions{SourceTaskIDs: []int64{task.ID}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := d.CreateTask("unrelated", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", " \t\n ", strings.Repeat("中", 8001)} {
		if _, err := d.AddFindingNote(ctx, fid, 0, "admin", body); !errors.Is(err, ErrFindingNoteInvalid) {
			t.Fatalf("invalid note accepted: %v", err)
		}
	}
	n, err := d.AddFindingNote(ctx, fid, task.ID, "admin", "  "+strings.Repeat("中", 8000)+"\n")
	if err != nil || len([]rune(n.Body)) != 8000 {
		t.Fatalf("unicode boundary %v", err)
	}
	second, err := d.AddFindingNote(ctx, fid, 0, "admin", "second")
	if err != nil {
		t.Fatal(err)
	}
	list, more, err := d.ListFindingNotes(ctx, fid, child.ID, 0, 1)
	if err != nil || !more || len(list) != 1 || list[0].ID != second.ID {
		t.Fatalf("page one %v %v %v", list, more, err)
	}
	list, more, err = d.ListFindingNotes(ctx, fid, child.ID, list[0].ID, 1)
	if err != nil || more || len(list) != 1 || list[0].ID != n.ID {
		t.Fatalf("page two %v %v %v", list, more, err)
	}
	if _, err = d.AddFindingNote(ctx, fid, child.ID, "admin", "child write"); !errors.Is(err, ErrFindingReadOnly) {
		t.Fatalf("inherited write %v", err)
	}
	if err = d.DeleteFindingNote(ctx, fid, child.ID, n.ID); !errors.Is(err, ErrFindingReadOnly) {
		t.Fatalf("inherited delete %v", err)
	}
	if _, _, err = d.ListFindingNotes(ctx, fid, other.ID, 0, 50); !errors.Is(err, ErrFindingUnavailable) {
		t.Fatalf("unrelated read %v", err)
	}
	if _, _, err = d.ListFindingNotes(ctx, 999999, 0, 0, 50); !errors.Is(err, ErrFindingNotFound) {
		t.Fatalf("missing finding %v", err)
	}
	global, err := d.AddFinding(0, 0, "TEST", "global", "low", "summary", "proof", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.AddFindingNote(ctx, global, 0, "admin", "global"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = d.ListFindingNotes(ctx, global, task.ID, 0, 50); !errors.Is(err, ErrFindingUnavailable) {
		t.Fatalf("source-less context %v", err)
	}
	if err = d.DeleteFindingNote(ctx, global, 0, n.ID); !errors.Is(err, ErrFindingNoteNotFound) {
		t.Fatalf("cross finding delete %v", err)
	}
	if err = d.DeleteFindingNote(ctx, fid, task.ID, n.ID); err != nil {
		t.Fatal(err)
	}
	// A queued archive blocks writes but retains readable notes.
	if _, err = d.Exec(`DELETE FROM task_relations WHERE task_id=$1`, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE tasks SET paused=true,queued=false WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.QueueTaskArchive(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.AddFindingNote(ctx, fid, 0, "admin", "archived"); !errors.Is(err, ErrTaskArchiveState) {
		t.Fatalf("archive note write %v", err)
	}
	if err = d.DeleteFindingNote(ctx, fid, 0, second.ID); !errors.Is(err, ErrTaskArchiveState) {
		t.Fatalf("archive note delete %v", err)
	}
	status := FindingFixed
	if err = d.PatchFindingManual(ctx, fid, 0, "admin", FindingManualPatch{Status: &status}); !errors.Is(err, ErrTaskArchiveState) {
		t.Fatalf("archive status write %v", err)
	}
	if _, _, err = d.ListFindingNotes(ctx, fid, 0, 0, 50); err != nil {
		t.Fatal(err)
	}
}

func TestFindingManualReviewHistoryAndAtomicRollback(t *testing.T) {
	d := isolatedFindingNotesDB(t)
	ctx := context.Background()
	f, err := d.AddFinding(0, 0, "TEST", "manual", "high", "summary", "immutable", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	confirmed := FindingConfirmed
	if err = d.PatchFindingManual(ctx, f, 0, "shared admin", FindingManualPatch{Status: &confirmed}); err != nil {
		t.Fatal(err)
	}
	first, err := d.GetFinding(f)
	if err != nil || first.ReviewedBy != "shared admin" || first.ReviewedAt == nil || first.ReviewedStatus != confirmed {
		t.Fatalf("manual snapshot %+v %v", first, err)
	}
	if err = d.PatchFindingManual(ctx, f, 0, "replay", FindingManualPatch{Status: &confirmed}); err != nil {
		t.Fatal(err)
	}
	replay, _ := d.GetFinding(f)
	if replay.ReviewedBy != first.ReviewedBy || !replay.ReviewedAt.Equal(*first.ReviewedAt) {
		t.Fatal("same status replay refreshed review")
	}
	if _, _, _, err = d.SetFindingStatusWithNotify(ctx, f, FindingFixed); err != nil {
		t.Fatal(err)
	}
	automatic, _ := d.GetFinding(f)
	if automatic.Status != FindingFixed || automatic.ReviewedStatus != confirmed || !automatic.ReviewedAt.Equal(*first.ReviewedAt) {
		t.Fatalf("automatic status overwrote last manual review: %+v", automatic)
	}
	var events int
	if err = d.QueryRow(`SELECT count(*) FROM notification_events WHERE finding_id=$1`, f).Scan(&events); err != nil || events != 2 {
		t.Fatalf("events %d %v", events, err)
	}
	// Rejecting the review row aborts the status update and its queued event too.
	if _, err = d.Exec(`ALTER TABLE findings ADD CONSTRAINT reject_fixture_actor CHECK (reviewed_by <> 'reject')`); err != nil {
		t.Fatal(err)
	}
	ignored := FindingIgnored
	if err = d.PatchFindingManual(ctx, f, 0, "reject", FindingManualPatch{Status: &ignored}); err == nil {
		t.Fatal("expected injected review failure")
	}
	after, _ := d.GetFinding(f)
	d.QueryRow(`SELECT count(*) FROM notification_events WHERE finding_id=$1`, f).Scan(&events)
	if after.Status != FindingFixed || after.ReviewedStatus != confirmed || events != 2 {
		t.Fatalf("transaction partially committed: %+v events=%d", after, events)
	}
	badSeverity := "bad"
	if err = d.PatchFindingManual(ctx, f, 0, "admin", FindingManualPatch{Status: &ignored, Severity: &badSeverity}); err == nil {
		t.Fatal("invalid patch accepted")
	}
	after, _ = d.GetFinding(f)
	if after.Status != FindingFixed || after.Evidence != "immutable" {
		t.Fatal("invalid patch changed finding")
	}

	// Preserve the existing notification SAVEPOINT behavior: an event-table
	// failure does not discard a successful manual triage/review.
	if _, err = d.Exec(`ALTER TABLE notification_events ADD CONSTRAINT reject_ignored_event CHECK (snapshot->>'to_status' <> 'ignored')`); err != nil {
		t.Fatal(err)
	}
	if err = d.PatchFindingManual(ctx, f, 0, "admin", FindingManualPatch{Status: &ignored}); err != nil {
		t.Fatal(err)
	}
	after, _ = d.GetFinding(f)
	if after.Status != ignored || after.ReviewedStatus != ignored || after.ReviewedBy != "admin" {
		t.Fatalf("notification failure prevented manual review %+v", after)
	}
}
