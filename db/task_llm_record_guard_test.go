package db

import (
	"strconv"
	"testing"
)

func TestTaskLLMRecordGuard(t *testing.T) {
	dsn := testDSN(t)
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	newTask := func(t *testing.T) *Task {
		t.Helper()
		task, err := d.CreateTask("body guard", "local database regression", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = d.Exec(`DELETE FROM llm_records WHERE task_id=$1`, strconv.FormatInt(task.ID, 10))
			_ = d.DeleteTask(task.ID)
		})
		return task
	}
	record := func(id int64) *LLMRecord {
		return &LLMRecord{ID: 771, TaskID: strconv.FormatInt(id, 10), Model: "body-guard", RequestBody: "local body"}
	}
	count := func(t *testing.T, id int64, want int) {
		t.Helper()
		var got int
		if err := d.QueryRow(`SELECT count(*) FROM llm_records WHERE task_id=$1`, strconv.FormatInt(id, 10)).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("body rows = %d, want %d", got, want)
		}
	}
	t.Run("writer commits before cascade removes body", func(t *testing.T) {
		task := newTask(t)
		blocker, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		if _, err := blocker.Exec(`LOCK TABLE llm_records IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		writer, writerPID := openSingleConnectionTestDB(t, dsn)
		written := make(chan error, 1)
		go func() { written <- writer.InsertTaskLLMRecord(record(task.ID), task.ID) }()
		if err := waitForBackendBlock(d, writerPID, written); err != nil {
			t.Fatal(err)
		}
		deleter, deletePID := openSingleConnectionTestDB(t, dsn)
		deleted := make(chan error, 1)
		go func() { _, err := deleter.DeleteTaskCascade(task.ID, false, false, true); deleted <- err }()
		if err := waitForBackendBlock(d, deletePID, deleted); err != nil {
			t.Fatal(err)
		}
		// 验证删除正在等待正文写入者持有的任务锁。
		var waitsForWriter bool
		if err := d.QueryRow(`SELECT $1 = ANY(pg_blocking_pids($2))`, writerPID, deletePID).Scan(&waitsForWriter); err != nil {
			t.Fatal(err)
		}
		if !waitsForWriter {
			t.Fatal("cascade did not wait for task body writer")
		}
		if err := blocker.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := waitForMutationResult(written); err != nil {
			t.Fatal(err)
		}
		if err := waitForMutationResult(deleted); err != nil {
			t.Fatal(err)
		}
		count(t, task.ID, 0)
	})
	t.Run("cascade commits before writer skips", func(t *testing.T) {
		task := newTask(t)
		locked, release := make(chan struct{}), make(chan struct{})
		deleted := make(chan error, 1)
		go func() {
			_, err := d.DeleteTaskCascadePrepared(task.ID, false, false, true, func(TaskDeletePreparation) error {
				close(locked)
				<-release
				return nil
			})
			deleted <- err
		}()
		<-locked
		writer, pid := openSingleConnectionTestDB(t, dsn)
		written := make(chan error, 1)
		r := record(task.ID)
		go func() { written <- writer.InsertTaskLLMRecord(r, task.ID) }()
		blockErr := waitForBackendBlock(d, pid, written)
		close(release)
		if blockErr != nil {
			t.Fatal(blockErr)
		}
		if err := waitForMutationResult(deleted); err != nil {
			t.Fatal(err)
		}
		if err := waitForMutationResult(written); err != nil {
			t.Fatal(err)
		}
		if r.ID != 771 {
			t.Fatalf("skipped record ID changed: %d", r.ID)
		}
		count(t, task.ID, 0)
	})
	for _, column := range []string{"deleted_at", "archived_at"} {
		t.Run(column, func(t *testing.T) {
			task := newTask(t)
			if _, err := d.Exec(`UPDATE tasks SET `+column+`=now() WHERE id=$1`, task.ID); err != nil {
				t.Fatal(err)
			}
			r := record(task.ID)
			if err := d.InsertTaskLLMRecord(r, task.ID); err != nil {
				t.Fatal(err)
			}
			if r.ID != 771 {
				t.Fatal("skipped record ID changed")
			}
			count(t, task.ID, 0)
		})
	}
	t.Run("keep existing bodies prevents late bodies", func(t *testing.T) {
		task := newTask(t)
		if err := d.InsertTaskLLMRecord(record(task.ID), task.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := d.DeleteTaskCascade(task.ID, false, false, false); err != nil {
			t.Fatal(err)
		}
		if err := d.InsertTaskLLMRecord(record(task.ID), task.ID); err != nil {
			t.Fatal(err)
		}
		count(t, task.ID, 1)
	})
	t.Run("explicit owner validation and missing task", func(t *testing.T) {
		for _, r := range []*LLMRecord{nil, {TaskID: "01"}, {TaskID: "exp-1"}, {TaskID: "2"}} {
			if err := d.InsertTaskLLMRecord(r, 1); err == nil {
				t.Fatalf("accepted invalid owner: %+v", r)
			}
		}
		if err := d.InsertTaskLLMRecord(&LLMRecord{TaskID: "0"}, 0); err == nil {
			t.Fatal("accepted zero owner")
		}
		r := record(9223372036854775807)
		if err := d.InsertTaskLLMRecord(r, 9223372036854775807); err != nil {
			t.Fatal(err)
		}
		if r.ID != 771 {
			t.Fatal("missing task changed ID")
		}
	})
	t.Run("legacy owners remain unrestricted", func(t *testing.T) {
		for _, owner := range []string{"9223372036854775807", "exp-body-guard", ""} {
			r := &LLMRecord{TaskID: owner, SessionID: "body-guard-legacy"}
			if err := d.InsertLLMRecord(r); err != nil {
				t.Fatal(err)
			}
		}
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM llm_records WHERE session_id='body-guard-legacy'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 3 {
			t.Fatalf("legacy rows = %d", n)
		}
		if _, err := d.Exec(`DELETE FROM llm_records WHERE session_id='body-guard-legacy'`); err != nil {
			t.Fatal(err)
		}
	})
}
