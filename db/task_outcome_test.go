package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"strconv"
	"testing"
)

func outcomeTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" || dsn == "invalid" {
		t.Skip("requires isolated ARTEX_PG_DSN fixture")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	return d
}

func outcomeTestTask(t *testing.T, d *DB) *Task {
	t.Helper()
	task, err := d.CreateTask("outcome fixture", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID)
		_, _ = d.Exec(`DELETE FROM findings WHERE task_id=$1`, task.ID)
		_ = d.DeleteTask(task.ID)
		_, _ = d.Exec(`DELETE FROM llm_usage WHERE task_id=$1 OR exploration_id=$2`, strconv.FormatInt(task.ID, 10), task.ExplorationID)
		_, _ = d.Exec(`DELETE FROM tool_usage WHERE task_id=$1 OR exploration_id=$2`, task.ID, task.ExplorationID)
		_, _ = d.Exec(`DELETE FROM skill_usage WHERE task_id=$1 OR exploration_id=$2`, task.ID, task.ExplorationID)
	})
	return task
}

func TestTaskOutcomeDatabaseUnavailable(t *testing.T) {
	for _, d := range []*DB{nil, {}} {
		if _, err := d.TaskOutcome(t.Context(), 1); err == nil {
			t.Fatal("missing database must return an error")
		}
	}
}

func TestArchivedTaskOutcomePartialAndIdentity(t *testing.T) {
	out, err := archivedTaskOutcome([]byte(`{"finding_stats":{"total":7,"high":7},"tokens":{"input_tokens":999},"tools":{"private-task-tool":3}}`), 42, "done", 9)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Partial || out.Source != "archive" || out.Findings.Total != 7 || out.Findings.High != 0 || out.Tokens.InputTokens != 0 || out.ToolCalls != 0 || out.WallSeconds != nil {
		t.Fatalf("legacy archive scope: %+v", out)
	}
	if !reflect.DeepEqual(out.Unavailable, []string{"findings.statuses", "findings.confirmed_severity", "retests", "tokens", "tool_calls", "skill_misses", "wall_seconds"}) {
		t.Fatalf("unavailable=%v", out.Unavailable)
	}
	for _, raw := range []string{`{`, `{"outcome":{"task_id":43,"schema_version":1}}`, `{"outcome":{"task_id":42,"schema_version":999}}`} {
		if _, err := archivedTaskOutcome([]byte(raw), 42, "done", 9); err == nil {
			t.Fatal("corrupt or mismatched archive must return an error")
		}
	}
	empty, err := archivedTaskOutcome([]byte(`{}`), 42, "done", 9)
	if err != nil || len(empty.Unavailable) != 8 {
		t.Fatalf("missing legacy total: %+v %v", empty, err)
	}
}

func TestTaskOutcomeScopedAndArchived(t *testing.T) {
	d := outcomeTestDB(t)
	task, other := outcomeTestTask(t, d), outcomeTestTask(t, d)
	ctx := t.Context()
	ids := []int64{}
	for _, row := range []struct{ status, severity string }{
		{"pending", "critical"}, {"in_progress", "high"}, {"confirmed", "high"},
		{"resolved", "medium"}, {"fixed", "critical"}, {"false_positive", "low"},
		{"ignored", "high"}, {"duplicate", "high"}, {"risk_accepted", "low"}, {"confirmed", ""},
	} {
		var id int64
		if err := d.QueryRow(`INSERT INTO findings(task_id,vulnclass,severity,status) VALUES($1,'fixture',$2,$3) RETURNING id`, task.ID, row.severity, row.status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := d.Exec(`INSERT INTO findings(task_id,vulnclass,severity,status) VALUES($1,'foreign','critical','confirmed')`, other.ID); err != nil {
		t.Fatal(err)
	}
	// 直接继承关系不改变结果产出的来源任务归属。
	if _, err := d.Exec(`INSERT INTO task_relations(task_id,source_task_id) VALUES($1,$2)`, task.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	for i, row := range []struct{ status, verdict string }{
		{"completed", "reproduced"}, {"completed", "fixed"}, {"completed", "inconclusive"},
		{"failed", "fixed"}, {"running", "fixed"}, {"stopped", "reproduced"}, {"pending", ""},
	} {
		if _, err := d.Exec(`INSERT INTO finding_retests(finding_id,status,verdict,snapshot) VALUES($1,$2,$3,'{}')`, ids[i], row.status, row.verdict); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		`INSERT INTO llm_usage(task_id,exploration_id,input_tokens,output_tokens,cache_read,cache_write,status) VALUES($1::bigint::text,$2,11,2,3,4,'ok'),($1::bigint::text,$2,13,3,5,6,'error'),(NULL,$2,17,5,7,8,'ok'),($3::bigint::text,$2,999,999,999,999,'ok')`,
		`INSERT INTO tool_usage(tool_key,task_id,exploration_id) VALUES('own',$1,$2),('retry',$1,$2),('legacy',NULL,$2),('foreign',$3,$2)`,
		`INSERT INTO skill_usage(skill,task_id,exploration_id,found) VALUES('found',$1,$2,true),('miss',$1,$2,false),('legacy',NULL,$2,false),('foreign',$3,$2,false)`,
	} {
		if _, err := d.Exec(query, task.ID, task.ExplorationID, other.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(`INSERT INTO activity(exploration_id,kind,input_tokens,output_tokens) VALUES($1,'result',999,999)`, task.ExplorationID); err != nil {
		t.Fatal(err)
	}
	out, err := d.TaskOutcome(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantFindings := TaskOutcomeFindings{Total: 10, Pending: 1, InProgress: 1, Confirmed: 2, Resolved: 1, Fixed: 1, FalsePositive: 1, Ignored: 1, Duplicate: 1, RiskAccepted: 1, Open: 2, ConfirmedTotal: 4, Critical: 1, High: 1, Medium: 1, Unclassified: 1}
	if out.Findings != wantFindings || out.Partial || out.Source != "live" || out.WallSeconds != nil {
		t.Fatalf("findings/lifecycle=%+v", out)
	}
	if out.Retests != (TaskOutcomeRetests{Total: 7, Pending: 1, Running: 1, Completed: 3, Failed: 1, Stopped: 1, Reproduced: 1, Fixed: 1, Inconclusive: 1}) {
		t.Fatalf("retests=%+v", out.Retests)
	}
	if out.Tokens != (TaskOutcomeTokens{Calls: 3, InputTokens: 41, OutputTokens: 10, CacheReadTokens: 15, CacheWriteTokens: 18}) || out.ToolCalls != 3 || out.SkillMisses != 2 {
		t.Fatalf("usage totals: %+v", out)
	}
	if _, err := d.Exec(`UPDATE tasks SET status='done',first_run_at='2026-01-01 00:00:00 UTC',completed_at='2026-01-01 00:01:00 UTC',paused=true WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchive(task.ID); !errors.Is(err, ErrTaskArchiveRetestActive) {
		t.Fatalf("active retests must block archiving: %v", err)
	}
	if _, err := d.Exec(`UPDATE finding_retests SET status='stopped',finished_at=now() WHERE finding_id IN (SELECT id FROM findings WHERE task_id=$1) AND status IN ('pending','running')`, task.ID); err != nil {
		t.Fatal(err)
	}
	out, err = d.TaskOutcome(ctx, task.ID)
	if err != nil || out.WallSeconds == nil || *out.WallSeconds != 60 {
		t.Fatalf("terminal wall time: %+v %v", out, err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := d.ClaimTaskArchiveJob(ctx)
	if err != nil || job == nil || job.ID != archive.ID {
		t.Fatalf("claim archive: %+v %v", job, err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "outcome-fixture-unused", "fixture", 1, 1); err != nil {
		t.Fatal(err)
	}
	cold, err := d.TaskOutcome(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := out
	want.Source = "archive"
	if !reflect.DeepEqual(cold, want) {
		t.Fatalf("archive outcome=%+v want %+v", cold, want)
	}
	var hot int
	if err := d.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, task.ID).Scan(&hot); err != nil || hot != 0 {
		t.Fatalf("archive must compact findings: %d %v", hot, err)
	}
	// 旧格式只读摘要仍可检索；全量结果查询不读取冷包文件。
	if _, err := d.Exec(`UPDATE task_archives SET aggregate_stats=aggregate_stats-'outcome' WHERE id=$1`, archive.ID); err != nil {
		t.Fatal(err)
	}
	legacy, err := d.TaskOutcome(ctx, task.ID)
	if err != nil || !legacy.Partial || legacy.Findings.Total != 10 || legacy.WallSeconds != nil {
		t.Fatalf("legacy archive: %+v %v", legacy, err)
	}
	if _, err := d.TaskOutcome(ctx, -1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing task error: %v", err)
	}
	if _, err := d.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.TaskOutcome(ctx, other.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted task error: %v", err)
	}
}

func TestTaskOutcomeWallClockStates(t *testing.T) {
	d := outcomeTestDB(t)
	task := outcomeTestTask(t, d)
	for _, row := range []struct {
		status    string
		completed any
		wantNull  bool
		want      int64
	}{
		{"running", nil, false, 3600}, {"done", nil, true, 0}, {"done", "2000-01-01 UTC", false, 0},
	} {
		if _, err := d.Exec(`UPDATE tasks SET status=$2,paused=true,first_run_at=now()-interval '1 hour',completed_at=$3 WHERE id=$1`, task.ID, row.status, row.completed); err != nil {
			t.Fatal(err)
		}
		out, err := d.TaskOutcome(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.wantNull {
			if out.WallSeconds != nil {
				t.Fatal("unknown terminal completion must be null")
			}
		} else if out.WallSeconds == nil || *out.WallSeconds < row.want || *out.WallSeconds > row.want+1 {
			t.Fatalf("wall clock status %s: %+v", row.status, out.WallSeconds)
		}
	}
}
