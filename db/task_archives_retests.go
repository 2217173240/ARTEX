package db

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

func ensureTaskRetestsQuiescent(tx *sql.Tx, taskID int64) error {
	var active bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM finding_retests r JOIN findings f ON f.id=r.finding_id
 WHERE f.task_id=$1 AND r.status IN ('pending','running'))`, taskID).Scan(&active); err != nil {
		return err
	}
	if active {
		return ErrTaskArchiveRetestActive
	}
	return nil
}

// 独立会话及其输出保留在热库；冷包保存复测原始快照、结论、证据和时间戳。
func restoreFindingRetestsTx(tx *sql.Tx, snapshot *TaskArchiveSnapshot) ([]string, error) {
	raw, present := snapshot.Tables["finding_retests"]
	if !present {
		if snapshot.FormatVersion >= 4 {
			return nil, errors.New("archive finding_retests table is missing")
		}
		return []string{"旧归档未保存复测历史，已有历史无法恢复；结果卡将标记复测统计不完整"}, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, errors.New("archive finding_retests must be an array")
	}
	rows, err := decodeArchiveRows(raw)
	if err != nil {
		return nil, err
	}
	if count, ok := snapshot.DataCounts["finding_retests"]; !ok || count != int64(len(rows)) {
		return nil, errors.New("archive finding_retests count mismatch")
	}
	warnings := []string{}
	seen := map[int64]bool{}
	seenConversations := map[int64]bool{}
	for _, row := range rows {
		id, ok := jsonInt64(row["id"])
		if !ok || id <= 0 || seen[id] {
			return nil, errors.New("archive retest id is invalid or duplicated")
		}
		seen[id] = true
		findingID, ok := jsonInt64(row["finding_id"])
		if !ok || findingID <= 0 {
			return nil, errors.New("archive retest finding id is invalid")
		}
		var owned bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM findings WHERE id=$1 AND task_id=$2)`, findingID, snapshot.TaskID).Scan(&owned); err != nil {
			return nil, err
		}
		if !owned {
			return nil, errors.New("archive retest references a finding outside its task")
		}
		status, _ := row["status"].(string)
		if status != "completed" && status != "failed" && status != "stopped" {
			return nil, errors.New("archive retest must have a terminal status")
		}
		if row["conversation_id"] != nil {
			cid, ok := jsonInt64(row["conversation_id"])
			if !ok || cid <= 0 || seenConversations[cid] {
				return nil, errors.New("archive retest conversation id is invalid or duplicated")
			}
			seenConversations[cid] = true
			var agentKey string
			var bindingRaw []byte
			err := tx.QueryRow(`SELECT agent_key,retest_binding FROM conversations WHERE id=$1 FOR KEY SHARE`, cid).Scan(&agentKey, &bindingRaw)
			if errors.Is(err, sql.ErrNoRows) {
				row["conversation_id"] = nil
				warnings = append(warnings, fmt.Sprintf("复测 %d 的独立会话 %d 已删除，已保留原始复测快照与结论并解除会话引用", id, cid))
				if _, err := tx.Exec(`UPDATE tasks SET retest_history_complete=false WHERE id=$1`, snapshot.TaskID); err != nil {
					return nil, err
				}
			} else if err != nil {
				return nil, err
			} else {
				if agentKey != FindingRetestAgentKey {
					return nil, errors.New("archive retest conversation belongs to another agent")
				}
				var binding struct {
					RetestID  int64 `json:"retest_id"`
					FindingID int64 `json:"finding_id"`
					TaskID    int64 `json:"task_id"`
				}
				if err := json.Unmarshal(bindingRaw, &binding); err != nil {
					return nil, fmt.Errorf("decode retest conversation binding: %w", err)
				}
				if binding.RetestID == 0 && binding.FindingID == 0 && binding.TaskID == 0 {
					row["conversation_id"] = nil
					warnings = append(warnings, fmt.Sprintf("复测 %d 的会话 %d 缺少可验证来源，已保留复测历史并解除会话引用", id, cid))
					if _, err := tx.Exec(`UPDATE tasks SET retest_history_complete=false WHERE id=$1`, snapshot.TaskID); err != nil {
						return nil, err
					}
				} else if binding.RetestID != id || binding.FindingID != findingID || binding.TaskID != snapshot.TaskID {
					return nil, errors.New("archive retest conversation provenance does not match its source")
				}
				var conflict bool
				if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM finding_retests WHERE conversation_id=$1)`, cid).Scan(&conflict); err != nil {
					return nil, err
				}
				if conflict {
					return nil, errors.New("archive retest conversation is already bound to a retest")
				}
			}
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO finding_retests SELECT * FROM json_populate_record(NULL::finding_retests,$1::json)`, string(encoded)); err != nil {
			return nil, err
		}
	}
	return warnings, nil
}
