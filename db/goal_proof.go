package db

import (
	"context"
	"database/sql"
	"fmt"
)

type GoalProofResult struct {
	Total  int
	Met    int
	AllMet bool
}

// ProveGoal 提交证据边、目标状态和当前全部目标的统计。
func (s *ExplorationStore) ProveGoal(ctx context.Context, goalID, evidenceID int64) (GoalProofResult, error) {
	var result GoalProofResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin goal proof: %w", err)
	}
	defer tx.Rollback()
	// 先定位来源，再按任务 ID 加锁；任务删除同样先锁任务、再删除图。
	// 节点所属探索在取得节点锁后重新核对，定位结果本身不构成授权。
	var evidenceExplorationID int64
	if err := tx.QueryRowContext(ctx, `SELECT exploration_id FROM exploration_nodes WHERE id=$1`, evidenceID).Scan(&evidenceExplorationID); err != nil {
		return result, fmt.Errorf("locate goal proof evidence: %w", err)
	}
	taskRows, err := tx.QueryContext(ctx, `SELECT id, exploration_id, deleted_at IS NULL FROM tasks
WHERE exploration_id IN ($1,$2) ORDER BY id FOR SHARE`, s.expID, evidenceExplorationID)
	if err != nil {
		return result, fmt.Errorf("lock goal proof tasks: %w", err)
	}
	var ownerTaskID, sourceTaskID int64
	for taskRows.Next() {
		var id, expID int64
		var live bool
		if err := taskRows.Scan(&id, &expID, &live); err != nil {
			taskRows.Close()
			return result, fmt.Errorf("read goal proof task: %w", err)
		}
		if live && expID == s.expID {
			ownerTaskID = id
		}
		if live && expID == evidenceExplorationID {
			sourceTaskID = id
		}
	}
	if err := taskRows.Err(); err != nil {
		taskRows.Close()
		return result, fmt.Errorf("read goal proof tasks: %w", err)
	}
	if err := taskRows.Close(); err != nil {
		return result, fmt.Errorf("close goal proof tasks: %w", err)
	}
	if evidenceExplorationID != s.expID {
		// 与 GetNodeWithSources 一致：双方任务存在，且具有直接来源关系。
		// 身份取自已锁定任务，关系锁也在图锁之前取得。
		var sourceID int64
		err := tx.QueryRowContext(ctx, `SELECT source_task_id FROM task_relations
WHERE task_id=$1 AND source_task_id=$2 FOR SHARE`, ownerTaskID, sourceTaskID).Scan(&sourceID)
		if err == sql.ErrNoRows {
			return result, fmt.Errorf("evidence %d is outside this exploration and its direct sources", evidenceID)
		}
		if err != nil {
			return result, fmt.Errorf("validate goal proof source: %w", err)
		}
	}
	var explorationID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM explorations WHERE id=$1 FOR UPDATE`, s.expID).Scan(&explorationID); err != nil {
		return result, fmt.Errorf("lock goal proof exploration: %w", err)
	}

	// 固定节点锁顺序，使并发证明使用相同的加锁顺序。
	rows, err := tx.QueryContext(ctx, `SELECT id, exploration_id, kind FROM exploration_nodes
WHERE id IN ($1,$2) ORDER BY id FOR UPDATE`, goalID, evidenceID)
	if err != nil {
		return result, fmt.Errorf("lock goal proof nodes: %w", err)
	}
	type proofNode struct {
		expID int64
		kind  string
	}
	nodes := make(map[int64]proofNode, 2)
	for rows.Next() {
		var id int64
		var node proofNode
		if err := rows.Scan(&id, &node.expID, &node.kind); err != nil {
			rows.Close()
			return result, fmt.Errorf("read goal proof node: %w", err)
		}
		nodes[id] = node
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return result, fmt.Errorf("read goal proof nodes: %w", readErr)
	}
	if closeErr != nil {
		return result, fmt.Errorf("close goal proof nodes: %w", closeErr)
	}
	goal, ok := nodes[goalID]
	if !ok || goal.expID != s.expID || goal.kind != KindGoal {
		return result, fmt.Errorf("goal %d is not a goal in this exploration", goalID)
	}
	evidence, ok := nodes[evidenceID]
	if !ok || evidence.expID != evidenceExplorationID || (evidence.kind != KindFact && evidence.kind != KindFinding) {
		return result, fmt.Errorf("evidence %d is not a fact or finding", evidenceID)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO exploration_edges(exploration_id, src_id, rel, dst_id)
VALUES ($1,$2,$3,$4) ON CONFLICT (exploration_id, src_id, rel, dst_id) DO NOTHING`, s.expID, evidenceID, RelProves, goalID); err != nil {
		return result, fmt.Errorf("insert goal proof edge: %w", err)
	}
	updated, err := tx.ExecContext(ctx, `UPDATE exploration_nodes
SET state='met', blocked_reason=NULL, content_version=content_version+1
WHERE id=$1 AND exploration_id=$2 AND kind='goal'`, goalID, s.expID)
	if err != nil {
		return result, fmt.Errorf("update proved goal: %w", err)
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return result, fmt.Errorf("check proved goal update: %w", err)
	}
	if n != 1 {
		return result, fmt.Errorf("update proved goal affected %d rows", n)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE state='met')
FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal'`, s.expID).Scan(&result.Total, &result.Met); err != nil {
		return GoalProofResult{}, fmt.Errorf("count proved goals: %w", err)
	}
	result.AllMet = result.Total > 0 && result.Total == result.Met
	if err := tx.Commit(); err != nil {
		return GoalProofResult{}, fmt.Errorf("commit goal proof: %w", err)
	}
	return result, nil
}
