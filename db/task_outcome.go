package db

// 任务结果度量参考 BaaRai/ARTEX PR #102，作者 BaaRai，AGPL-3.0。
// 2026-10-19：适配任务归属、调用账本、复测终态和冷归档契约。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type TaskOutcomeFindings struct {
	Total          int64 `json:"total"`
	Pending        int64 `json:"pending"`
	InProgress     int64 `json:"in_progress"`
	Confirmed      int64 `json:"confirmed"`
	Resolved       int64 `json:"resolved"`
	Fixed          int64 `json:"fixed"`
	FalsePositive  int64 `json:"false_positive"`
	Ignored        int64 `json:"ignored"`
	Duplicate      int64 `json:"duplicate"`
	RiskAccepted   int64 `json:"risk_accepted"`
	Open           int64 `json:"open"`
	ConfirmedTotal int64 `json:"confirmed_total"`
	Critical       int64 `json:"critical"`
	High           int64 `json:"high"`
	Medium         int64 `json:"medium"`
	Low            int64 `json:"low"`
	Unclassified   int64 `json:"unclassified"`
}

type TaskOutcomeRetests struct {
	Total        int64 `json:"total"`
	Pending      int64 `json:"pending"`
	Running      int64 `json:"running"`
	Completed    int64 `json:"completed"`
	Failed       int64 `json:"failed"`
	Stopped      int64 `json:"stopped"`
	Reproduced   int64 `json:"reproduced"`
	Fixed        int64 `json:"fixed"`
	Inconclusive int64 `json:"inconclusive"`
}

type TaskOutcomeTokens struct {
	Calls            int64 `json:"calls"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

type TaskOutcomeScope struct {
	Findings string `json:"findings"`
	Retests  string `json:"retests"`
	Tokens   string `json:"tokens"`
	Tools    string `json:"tools"`
	Skills   string `json:"skills"`
	Time     string `json:"time"`
	Cost     string `json:"cost"`
}

// TaskOutcome 保留每个计量维度的范围。Unavailable 中的维度不可把零值解释为测量结果。
type TaskOutcome struct {
	SchemaVersion int                 `json:"schema_version"`
	TaskID        int64               `json:"task_id"`
	Status        string              `json:"status"`
	CategoryID    int64               `json:"category_id"`
	Source        string              `json:"source"`
	Partial       bool                `json:"partial"`
	Unavailable   []string            `json:"unavailable"`
	Findings      TaskOutcomeFindings `json:"findings"`
	Retests       TaskOutcomeRetests  `json:"retests"`
	Tokens        TaskOutcomeTokens   `json:"tokens"`
	ToolCalls     int64               `json:"tool_calls"`
	SkillMisses   int64               `json:"skill_misses"`
	WallSeconds   *int64              `json:"wall_seconds"`
	Scope         TaskOutcomeScope    `json:"scope"`
}

func taskOutcomeScope() TaskOutcomeScope {
	return TaskOutcomeScope{
		Findings: "task-owned finding rows; excludes inherited findings and case deduplication; status fields are disjoint; derived open=pending+in_progress, confirmed_total=confirmed+resolved+fixed; confirmed severity counts confirmed_total only",
		Retests:  "all retest attempts of task-owned findings; verdicts count completed attempts only; repeated attempts are separate",
		Tokens:   "recorded model completion calls, including recorded failed completions; internal HTTP and empty-response retries cannot be distinguished; exact task_id, or exploration_id only when task_id is absent; excludes independent retest conversations; missing provider usage is stored as zero and cannot be distinguished from zero usage; lost ledger writes cannot be recovered",
		Tools:    "tool_usage rows; exact task_id, or exploration_id only when task_id is absent; excludes independent retest conversations",
		Skills:   "skill_usage rows with found=false; exact task_id, or exploration_id only when task_id is absent; excludes independent retest conversations",
		Time:     "elapsed wall time from first_run_at to completed_at or observation; includes pauses and waiting; null before first run or when terminal completion time is unknown",
		Cost:     "unknown; token counts are usage, not actual fees; prices and billing adjustments are not recorded",
	}
}

// TaskOutcome 在同一个只读快照中选择热数据或冷归档，避免归档切换时重复计算或显示空数据。
func (d *DB) TaskOutcome(ctx context.Context, taskID int64) (TaskOutcome, error) {
	if d == nil || d.DB == nil {
		return TaskOutcome{}, errors.New("task outcome database unavailable")
	}
	tx, err := d.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return TaskOutcome{}, err
	}
	defer tx.Rollback()
	var archived bool
	var status string
	var categoryID int64
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT t.archived_at IS NOT NULL,
 CASE WHEN t.archived_at IS NOT NULL THEN a.original_status ELSE t.status END,
 COALESCE(CASE WHEN t.archived_at IS NOT NULL THEN a.category_id_snapshot ELSE t.category_id END,0),
 COALESCE(a.aggregate_stats,'{}'::jsonb)
 FROM tasks t LEFT JOIN task_archives a ON a.task_id=t.id
 WHERE t.id=$1 AND (t.deleted_at IS NULL OR (t.archived_at IS NOT NULL AND a.id IS NOT NULL))`, taskID).
		Scan(&archived, &status, &categoryID, &raw)
	if err != nil {
		return TaskOutcome{}, err
	}
	var out TaskOutcome
	if archived {
		out, err = archivedTaskOutcome(raw, taskID, status, categoryID)
	} else {
		out, err = liveTaskOutcome(ctx, tx, taskID)
	}
	if err != nil {
		return TaskOutcome{}, err
	}
	if err = tx.Commit(); err != nil {
		return TaskOutcome{}, err
	}
	return out, nil
}

// liveTaskOutcome 同样供归档事务调用；所有子查询独立聚合，调用账本每行只计一次。
func liveTaskOutcome(ctx context.Context, tx *sql.Tx, taskID int64) (TaskOutcome, error) {
	out := TaskOutcome{SchemaVersion: 1, Source: "live", Unavailable: []string{}, Scope: taskOutcomeScope()}
	var findings, retests, tokens []byte
	var retestHistoryComplete bool
	err := tx.QueryRowContext(ctx, `SELECT t.id,t.status,COALESCE(t.category_id,0),
 (SELECT jsonb_build_object(
 'total',count(*),'pending',count(*) FILTER(WHERE status='pending'),
 'in_progress',count(*) FILTER(WHERE status='in_progress'),'confirmed',count(*) FILTER(WHERE status='confirmed'),
 'resolved',count(*) FILTER(WHERE status='resolved'),'fixed',count(*) FILTER(WHERE status='fixed'),
 'false_positive',count(*) FILTER(WHERE status='false_positive'),'ignored',count(*) FILTER(WHERE status='ignored'),
 'duplicate',count(*) FILTER(WHERE status='duplicate'),'risk_accepted',count(*) FILTER(WHERE status='risk_accepted'),
 'open',count(*) FILTER(WHERE status IN ('pending','in_progress')),
 'confirmed_total',count(*) FILTER(WHERE status IN ('confirmed','resolved','fixed')),
 'critical',count(*) FILTER(WHERE status IN ('confirmed','resolved','fixed') AND severity='critical'),
 'high',count(*) FILTER(WHERE status IN ('confirmed','resolved','fixed') AND severity='high'),
 'medium',count(*) FILTER(WHERE status IN ('confirmed','resolved','fixed') AND severity='medium'),
 'low',count(*) FILTER(WHERE status IN ('confirmed','resolved','fixed') AND severity='low'),
 'unclassified',count(*) FILTER(WHERE status IN ('confirmed','resolved','fixed') AND COALESCE(severity,'') NOT IN ('critical','high','medium','low')))
 FROM findings WHERE task_id=t.id),
 (SELECT jsonb_build_object('total',count(*),'pending',count(*) FILTER(WHERE r.status='pending'),
 'running',count(*) FILTER(WHERE r.status='running'),'completed',count(*) FILTER(WHERE r.status='completed'),
 'failed',count(*) FILTER(WHERE r.status='failed'),'stopped',count(*) FILTER(WHERE r.status='stopped'),
 'reproduced',count(*) FILTER(WHERE r.status='completed' AND verdict='reproduced'),
 'fixed',count(*) FILTER(WHERE r.status='completed' AND verdict='fixed'),
 'inconclusive',count(*) FILTER(WHERE r.status='completed' AND verdict='inconclusive'))
 FROM finding_retests r JOIN findings f ON f.id=r.finding_id WHERE f.task_id=t.id),
 (SELECT jsonb_build_object('calls',count(*),'input_tokens',COALESCE(sum(input_tokens),0),
 'output_tokens',COALESCE(sum(output_tokens),0),'cache_read_tokens',COALESCE(sum(cache_read),0),
 'cache_write_tokens',COALESCE(sum(cache_write),0)) FROM llm_usage
 WHERE task_id=t.id::text OR (COALESCE(task_id,'')='' AND exploration_id=t.exploration_id)),
 (SELECT count(*) FROM tool_usage WHERE task_id=t.id OR (task_id IS NULL AND exploration_id=t.exploration_id)),
 (SELECT count(*) FROM skill_usage WHERE NOT found AND (task_id=t.id OR (task_id IS NULL AND exploration_id=t.exploration_id))),
 CASE WHEN t.first_run_at IS NULL OR (t.status IN ('done','failed','timeout') AND t.completed_at IS NULL) THEN NULL
 ELSE GREATEST(0,floor(EXTRACT(EPOCH FROM (COALESCE(t.completed_at,now())-t.first_run_at))))::bigint END
 ,t.retest_history_complete FROM tasks t WHERE t.id=$1 AND t.deleted_at IS NULL`, taskID).
		Scan(&out.TaskID, &out.Status, &out.CategoryID, &findings, &retests, &tokens, &out.ToolCalls, &out.SkillMisses, &out.WallSeconds, &retestHistoryComplete)
	if err != nil {
		return TaskOutcome{}, err
	}
	for _, item := range []struct {
		raw  []byte
		dest any
	}{{findings, &out.Findings}, {retests, &out.Retests}, {tokens, &out.Tokens}} {
		if err = json.Unmarshal(item.raw, item.dest); err != nil {
			return TaskOutcome{}, err
		}
	}
	if !retestHistoryComplete {
		out.Partial = true
		out.Unavailable = append(out.Unavailable, "retests")
	}
	return out, nil
}

func archivedTaskOutcome(raw []byte, taskID int64, status string, categoryID int64) (TaskOutcome, error) {
	var aggregate struct {
		Outcome      *TaskOutcome `json:"outcome"`
		FindingStats struct {
			Total *int64 `json:"total"`
		} `json:"finding_stats"`
	}
	if err := json.Unmarshal(raw, &aggregate); err != nil {
		return TaskOutcome{}, fmt.Errorf("decode archived task outcome: %w", err)
	}
	if aggregate.Outcome != nil {
		out := *aggregate.Outcome
		if out.TaskID != taskID {
			return TaskOutcome{}, errors.New("archived task outcome identity mismatch")
		}
		if out.SchemaVersion != 1 {
			return TaskOutcome{}, errors.New("archived task outcome schema is unsupported")
		}
		out.Source = "archive"
		return out, nil
	}
	// 旧归档未保存状态分布和复测，调用总量使用较宽的旧归属条件，不能推断成新计量结果。
	out := TaskOutcome{SchemaVersion: 1, TaskID: taskID, Status: status, CategoryID: categoryID, Source: "archive", Partial: true,
		Unavailable: []string{"findings.statuses", "findings.confirmed_severity", "retests", "tokens", "tool_calls", "skill_misses", "wall_seconds"}, Scope: taskOutcomeScope()}
	if aggregate.FindingStats.Total != nil {
		out.Findings.Total = *aggregate.FindingStats.Total
	} else {
		out.Unavailable = append(out.Unavailable, "findings.total")
	}
	return out, nil
}
