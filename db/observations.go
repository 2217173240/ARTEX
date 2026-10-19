package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ObservationInput references existing local evidence. Source metadata and time
// are resolved by the server; callers cannot supply a captured flag or timestamp.
type ObservationInput struct {
	TaskID     int64   `json:"-"`
	IntentID   int64   `json:"intent_id"`
	Summary    string  `json:"summary"`
	Detail     string  `json:"detail"`
	SourceKind string  `json:"source_kind"`
	SourceID   string  `json:"source_id"`
	ContextKey string  `json:"context_key"`
	AssetIDs   []int64 `json:"asset_ids"`
	Worker     string  `json:"-"`
}

type ObservationPayload struct {
	SchemaVersion    int             `json:"schema_version"`
	Summary          string          `json:"summary"`
	Detail           string          `json:"detail,omitempty"`
	IntentID         int64           `json:"intent_id"`
	SourceKind       string          `json:"source_kind"`
	SourceID         string          `json:"source_id,omitempty"`
	SourceTime       time.Time       `json:"source_time"`
	SourceHash       string          `json:"source_hash"`
	SourceSnapshot   json.RawMessage `json:"source_snapshot"`
	ContextKey       string          `json:"context_key"`
	CaptureStatus    string          `json:"capture_status"`
	ContextAssurance string          `json:"context_assurance"`
	SourceScope      string          `json:"source_scope"`
}

func evidenceHash(v any) string {
	raw, _ := json.Marshal(v)
	// JSONB and portable manifests can reorder object fields. Canonicalize
	// objects while preserving integer precision before hashing their content.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) == nil {
		raw, _ = json.Marshal(value)
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func ValidateObservationSource(p ObservationPayload) error {
	if len(p.SourceSnapshot) == 0 || p.SourceHash != evidenceHash(p.SourceSnapshot) {
		return errors.New("observation source hash mismatch")
	}
	return nil
}

func (s *ExplorationStore) lockEvidenceScope(tx *sql.Tx, taskID, intentID int64) error {
	if err := LockTaskEvidenceTx(tx, taskID); err != nil {
		return err
	}
	if taskID > 0 {
		var exp int64
		if err := tx.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, taskID).Scan(&exp); err != nil {
			return err
		}
		if exp != s.expID {
			return errors.New("task 与 exploration 不匹配")
		}
	} else {
		// A store associated with a task always uses its archive/write boundary,
		// including callers that do not explicitly pass TaskID.
		var actual int64
		err := tx.QueryRow(`SELECT id FROM tasks WHERE exploration_id=$1`, s.expID).Scan(&actual)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if actual > 0 {
			if err := LockTaskEvidenceTx(tx, actual); err != nil {
				return err
			}
		}
	}
	if intentID > 0 {
		var id int64
		if err := tx.QueryRow(`SELECT id FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent' FOR SHARE`, intentID, s.expID).Scan(&id); err != nil {
			return fmt.Errorf("intent_id 必须是本任务的意图: %w", err)
		}
	}
	return nil
}

func (s *ExplorationStore) insertEvidenceNode(tx *sql.Tx, kind string, payload any, state, worker string, intent int64, anchors []int64) (int64, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin) VALUES($1,$2,$3,5,$4,$5) RETURNING id`, s.expID, kind, string(jsonbClean(raw)), state, worker).Scan(&id)
	if err != nil {
		return 0, err
	}
	for _, anchor := range anchors {
		if _, err := tx.Exec(`INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, anchor); err != nil {
			return 0, err
		}
	}
	if intent > 0 {
		if err := s.linkEvidence(tx, intent, RelYields, id); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func (s *ExplorationStore) linkEvidence(tx *sql.Tx, from int64, rel string, to int64) error {
	_, err := tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, s.expID, from, rel, to)
	return err
}

func (s *ExplorationStore) evidenceNodeTx(tx *sql.Tx, id int64, anchors []int64) (*Node, error) {
	n, err := scanNode(tx.QueryRow(`SELECT `+nodeCols+` FROM exploration_nodes WHERE exploration_id=$1 AND id=$2`, s.expID, id))
	if err != nil {
		return nil, err
	}
	n.Anchors = append([]int64(nil), anchors...)
	return n, nil
}

func (s *ExplorationStore) RecordObservation(ctx context.Context, in ObservationInput) (*Node, error) {
	in.Summary = strings.TrimSpace(in.Summary)
	in.ContextKey = strings.TrimSpace(in.ContextKey)
	if strings.TrimSpace(in.Summary) == "" || strings.TrimSpace(in.ContextKey) == "" || in.IntentID <= 0 {
		return nil, errors.New("summary/context_key/intent_id 必须填写")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Use the same lock order as finding writes, GC and archive body copies.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, findingEvidenceLockKey); err != nil {
		return nil, err
	}
	if err = s.lockEvidenceScope(tx, in.TaskID, in.IntentID); err != nil {
		return nil, err
	}
	p := ObservationPayload{SchemaVersion: 1, Summary: in.Summary, Detail: in.Detail, IntentID: in.IntentID, ContextKey: in.ContextKey, SourceID: strings.TrimSpace(in.SourceID), SourceKind: in.SourceKind, CaptureStatus: "captured"}
	p.ContextAssurance = "declared"
	switch in.SourceKind {
	case "", "model_recorded":
		if p.SourceID != "" {
			return nil, errors.New("model_recorded 不接受 source_id")
		}
		p.SourceKind = "model_recorded"
		p.SourceScope = "model_text"
		p.CaptureStatus = "model_recorded"
		p.SourceTime = time.Now().UTC()
		p.SourceSnapshot, _ = json.Marshal(map[string]any{"summary": in.Summary, "detail": in.Detail})
	case "activity":
		p.SourceScope = "saved_tool_result"
		id, e := strconv.ParseInt(p.SourceID, 10, 64)
		if e != nil {
			return nil, errors.New("activity source_id 必须是整数")
		}
		p.SourceID = strconv.FormatInt(id, 10)
		var raw []byte
		// Tool result rows are written by the runtime capture stream. A matching
		// invocation excludes final model prose and orphaned/user-supplied rows.
		err = tx.QueryRow(`SELECT to_jsonb(a),a.created_at FROM activity a WHERE a.id=$1 AND a.exploration_id=$2 AND a.kind='tool_result' AND a.node_id=$3 AND a.tool NOT IN ('record_observation','audit_fact_validity','record_fact') AND EXISTS (SELECT 1 FROM activity u WHERE u.exploration_id=a.exploration_id AND u.node_id=a.node_id AND u.kind='tool_use' AND u.tool_use_id=a.tool_use_id AND u.tool=a.tool AND u.id<a.id)`, id, s.expID, in.IntentID).Scan(&raw, &p.SourceTime)
		if err != nil {
			return nil, fmt.Errorf("来源必须是本意图已有的真实工具结果: %w", err)
		}
		var source struct {
			Metadata map[string]any `json:"metadata"`
		}
		if err = json.Unmarshal(raw, &source); err != nil {
			return nil, err
		}
		if source.Metadata["result_disposition"] == "interrupted" || source.Metadata["result_disposition"] == "not_invoked" {
			return nil, errors.New("中断或未执行回执不能作为已执行观察")
		}
		if source.Metadata["execution_provenance"] != "runtime" || source.Metadata["result_disposition"] != "actual" {
			p.CaptureStatus = "legacy_unverified"
		}
		p.SourceSnapshot = raw
	case "traffic_snapshot":
		p.SourceScope = "persistent_traffic_snapshot"
		if len(p.SourceID) != 64 {
			return nil, errors.New("snapshot ID 必须是64位小写SHA256")
		}
		if value, e := hex.DecodeString(p.SourceID); e != nil || hex.EncodeToString(value) != p.SourceID {
			return nil, errors.New("snapshot ID 必须是64位小写SHA256")
		}
		var raw []byte
		var captured int64
		// A snapshot is task-owned through the existing finding binding. Global
		// traffic IDs and another task's finding never establish ownership.
		err = tx.QueryRow(`SELECT to_jsonb(s),s.captured_at FROM traffic_evidence_snapshots s WHERE s.id=$1 AND EXISTS(SELECT 1 FROM finding_traffic_bindings b JOIN findings f ON f.id=b.finding_id JOIN tasks t ON t.id=f.task_id WHERE b.snapshot_id=s.id AND t.exploration_id=$2)`, p.SourceID, s.expID).Scan(&raw, &captured)
		if err != nil {
			return nil, fmt.Errorf("流量快照必须已绑定本任务: %w", err)
		}
		var snapshot TrafficEvidenceSnapshot
		if err = json.Unmarshal(raw, &snapshot); err != nil {
			return nil, err
		}
		if TrafficSnapshotID(snapshot) != snapshot.ID {
			return nil, errors.New("流量快照哈希不匹配")
		}
		if captured <= 0 {
			return nil, errors.New("流量缺少实际采集时间")
		}
		p.SourceTime = time.Unix(captured, 0).UTC()
		p.SourceSnapshot = raw
	default:
		return nil, errors.New("source_kind 必须是 model_recorded/activity/traffic_snapshot")
	}
	p.SourceHash = evidenceHash(p.SourceSnapshot)
	id, err := s.insertEvidenceNode(tx, KindObservation, p, "recorded", in.Worker, in.IntentID, in.AssetIDs)
	if err != nil {
		return nil, err
	}
	node, err := s.evidenceNodeTx(tx, id, in.AssetIDs)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return node, nil
}

// EvidencePage returns local evidence nodes; linked tasks remain read-only and
// cannot accidentally supply an observation or qualification for this task.
type EvidencePage struct {
	Nodes      []*Node `json:"nodes"`
	NextBefore int64   `json:"next_before,omitempty"`
}

func (s *ExplorationStore) ListEvidence(kind, q string, limit int, before int64) (*EvidencePage, error) {
	if kind != "" && kind != KindObservation && kind != KindValidityGate && kind != KindFact {
		return nil, errors.New("invalid evidence kind")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM exploration_nodes WHERE exploration_id=$1 AND kind IN ('observation','validity_gate','fact') AND ($2='' OR kind=$2) AND ($3='' OR payload::text ILIKE '%'||$3||'%') AND ($4=0 OR id<$4) ORDER BY id DESC LIMIT $5`, s.expID, kind, q, before, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes, err := scanNodes(rows)
	if err != nil {
		return nil, err
	}
	out := &EvidencePage{Nodes: nodes}
	if out.Nodes == nil {
		out.Nodes = []*Node{}
	}
	if len(nodes) > limit {
		out.Nodes = nodes[:limit]
		out.NextBefore = nodes[limit-1].ID
	}
	return out, nil
}

type EvidenceDetail struct {
	Node  *Node   `json:"node"`
	Nodes []*Node `json:"nodes"`
	Edges []Edge  `json:"edges"`
}

func (s *ExplorationStore) EvidenceDetail(id int64) (*EvidenceDetail, error) {
	n, err := s.GetNode(id)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, sql.ErrNoRows
	}
	out := &EvidenceDetail{Node: n, Nodes: []*Node{n}, Edges: []Edge{}}
	edges, err := s.EdgesTouching([]int64{id})
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{id: true}
	for _, e := range edges {
		if e.From == id || e.To == id {
			out.Edges = append(out.Edges, e)
			for _, other := range []int64{e.From, e.To} {
				if !seen[other] {
					nn, er := s.GetNode(other)
					if er != nil {
						return nil, er
					}
					out.Nodes = append(out.Nodes, nn)
					seen[other] = true
				}
			}
		}
	}
	return out, nil
}
