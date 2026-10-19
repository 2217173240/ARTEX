package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const EvidenceFreshness = 24 * time.Hour

type QualificationInput struct {
	TaskID         int64   `json:"-"`
	IntentID       int64   `json:"intent_id"`
	Summary        string  `json:"summary"`
	Detail         string  `json:"detail"`
	ConclusionKind string  `json:"conclusion_kind"`
	ContextKey     string  `json:"context_key"`
	ObservationIDs []int64 `json:"observation_ids"`
	Worker         string  `json:"-"`
}

type ObservationVersion struct {
	ID         int64     `json:"id"`
	Version    int       `json:"version"`
	Hash       string    `json:"hash"`
	SourceTime time.Time `json:"source_time"`
}

type QualificationPayload struct {
	SchemaVersion    int                  `json:"schema_version"`
	Summary          string               `json:"summary"`
	Detail           string               `json:"detail,omitempty"`
	ConclusionKind   string               `json:"conclusion_kind"`
	ContextKey       string               `json:"context_key"`
	IntentID         int64                `json:"intent_id"`
	ClaimHash        string               `json:"claim_hash"`
	ObservationIDs   []int64              `json:"observation_ids"`
	Observations     []ObservationVersion `json:"observations"`
	Qualification    string               `json:"qualification"`
	SemanticStatus   string               `json:"semantic_status"`
	ContextAssurance string               `json:"context_assurance"`
	IntegrityScope   string               `json:"integrity_scope"`
	Reasons          []string             `json:"reasons"`
	CheckedAt        time.Time            `json:"checked_at"`
	ExpiresAt        time.Time            `json:"expires_at"`
}

func normalizedClaim(in QualificationInput) (QualificationInput, error) {
	in.Summary = strings.TrimSpace(in.Summary)
	in.ContextKey = strings.TrimSpace(in.ContextKey)
	if in.Summary == "" || in.ContextKey == "" || in.IntentID <= 0 {
		return in, errors.New("summary/context_key/intent_id 必须填写")
	}
	switch in.ConclusionKind {
	case "observation", "inference", "negative", "inconclusive", "security_claim":
	default:
		return in, errors.New("无效 conclusion_kind")
	}
	ids := append([]int64(nil), in.ObservationIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	in.ObservationIDs = []int64{}
	for _, id := range ids {
		if id <= 0 {
			return in, errors.New("observation_id 必须大于零")
		}
		if len(in.ObservationIDs) == 0 || in.ObservationIDs[len(in.ObservationIDs)-1] != id {
			in.ObservationIDs = append(in.ObservationIDs, id)
		}
	}
	return in, nil
}

func claimHash(in QualificationInput, refs []ObservationVersion) string {
	return evidenceHash(struct {
		Intent                         int64
		Summary, Detail, Kind, Context string
		Refs                           []ObservationVersion
	}{in.IntentID, in.Summary, in.Detail, in.ConclusionKind, in.ContextKey, refs})
}

func (s *ExplorationStore) qualifyTx(tx *sql.Tx, in QualificationInput) (QualificationPayload, error) {
	p := QualificationPayload{SchemaVersion: 1, Summary: in.Summary, Detail: in.Detail, ConclusionKind: in.ConclusionKind, ContextKey: in.ContextKey, IntentID: in.IntentID, ObservationIDs: in.ObservationIDs, Observations: []ObservationVersion{}, Qualification: "ready_for_review", SemanticStatus: "unknown", Reasons: []string{}, CheckedAt: time.Now().UTC()}
	p.ContextAssurance = "declared"
	p.IntegrityScope = "saved_source_record"
	if len(in.ObservationIDs) == 0 {
		p.Reasons = append(p.Reasons, "缺少原始观察引用")
	}
	for _, id := range in.ObservationIDs {
		var raw []byte
		var ver int
		if err := tx.QueryRow(`SELECT payload,content_version FROM exploration_nodes WHERE exploration_id=$1 AND id=$2 AND kind='observation' FOR SHARE`, s.expID, id).Scan(&raw, &ver); err != nil {
			return p, fmt.Errorf("observation %d 必须属于本任务: %w", id, err)
		}
		var o ObservationPayload
		if err := json.Unmarshal(raw, &o); err != nil {
			return p, err
		}
		if o.IntentID != in.IntentID || o.ContextKey != in.ContextKey {
			return p, fmt.Errorf("observation %d 的 intent/context 不匹配", id)
		}
		p.Observations = append(p.Observations, ObservationVersion{ID: id, Version: ver, Hash: evidenceHash(json.RawMessage(raw)), SourceTime: o.SourceTime})
		if o.SourceKind == "model_recorded" || o.CaptureStatus != "captured" {
			p.Reasons = append(p.Reasons, fmt.Sprintf("observation %d 为模型录入，缺少独立捕获来源", id))
		}
		if len(o.SourceSnapshot) == 0 || evidenceHash(o.SourceSnapshot) != o.SourceHash {
			p.Reasons = append(p.Reasons, fmt.Sprintf("observation %d 来源完整性校验失败", id))
		}
		if o.SourceKind == "activity" {
			var a Activity
			if err := json.Unmarshal(o.SourceSnapshot, &a); err != nil {
				return p, err
			}
			if a.IsError {
				p.Reasons = append(p.Reasons, fmt.Sprintf("observation %d 工具执行错误", id))
			}
		}
		if o.SourceTime.IsZero() || o.SourceTime.After(p.CheckedAt) || p.CheckedAt.Sub(o.SourceTime) > EvidenceFreshness {
			p.Reasons = append(p.Reasons, fmt.Sprintf("observation %d 来源时间缺失、过期或位于未来", id))
		}
		exp := o.SourceTime.Add(EvidenceFreshness)
		if p.ExpiresAt.IsZero() || exp.Before(p.ExpiresAt) {
			p.ExpiresAt = exp
		}
	}
	if len(p.Reasons) > 0 {
		p.Qualification = "insufficient"
	} else if in.ConclusionKind != "observation" {
		p.Qualification = "unknown"
	}
	p.Reasons = append(p.Reasons, "机器仅核查已保存来源的完整性、任务/意图归属、上下文声明一致性与时间；目标、身份、对象和业务语义仍需审阅")
	p.ClaimHash = claimHash(in, p.Observations)
	return p, nil
}

func (s *ExplorationStore) AuditFactValidity(ctx context.Context, in QualificationInput) (*Node, error) {
	in, err := normalizedClaim(in)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = s.lockEvidenceScope(tx, in.TaskID, in.IntentID); err != nil {
		return nil, err
	}
	p, err := s.qualifyTx(tx, in)
	if err != nil {
		return nil, err
	}
	id, err := s.insertEvidenceNode(tx, KindValidityGate, p, p.Qualification, in.Worker, in.IntentID, nil)
	if err != nil {
		return nil, err
	}
	for _, obs := range in.ObservationIDs {
		if err = s.linkEvidence(tx, obs, RelSupports, id); err != nil {
			return nil, err
		}
	}
	node, err := s.evidenceNodeTx(tx, id, nil)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return node, nil
}

type FactEvidenceInput struct {
	QualificationInput
	AuditID    int64   `json:"audit_id"`
	Evidence   string  `json:"evidence"`
	Confidence string  `json:"confidence"`
	AssetIDs   []int64 `json:"asset_ids"`
}

func (s *ExplorationStore) RecordFactWithEvidence(ctx context.Context, in FactEvidenceInput) (int64, error) {
	if strings.TrimSpace(in.Summary) == "" {
		return 0, errors.New("summary 不能为空")
	}
	structured := in.AuditID > 0 || len(in.ObservationIDs) > 0 || in.ConclusionKind != "" || in.ContextKey != ""
	var err error
	if structured {
		in.QualificationInput, err = normalizedClaim(in.QualificationInput)
		if err != nil {
			return 0, err
		}
		if in.AuditID <= 0 {
			return 0, errors.New("结构化结论必须提供匹配的 audit_id")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = s.lockEvidenceScope(tx, in.TaskID, in.IntentID); err != nil {
		return 0, err
	}
	payload := map[string]any{"summary": in.Summary, "detail": in.Detail, "evidence": in.Evidence, "confidence": in.Confidence, "qualification": "legacy_unchecked"}
	if structured {
		var raw []byte
		if err = tx.QueryRow(`SELECT payload FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='validity_gate' FOR UPDATE`, in.AuditID, s.expID).Scan(&raw); err != nil {
			return 0, fmt.Errorf("audit_id 必须属于本任务: %w", err)
		}
		var saved QualificationPayload
		if err = json.Unmarshal(raw, &saved); err != nil {
			return 0, err
		}
		fresh, err := s.qualifyTx(tx, in.QualificationInput)
		if err != nil {
			return 0, err
		}
		if saved.ClaimHash != fresh.ClaimHash || saved.IntentID != in.IntentID || saved.ContextKey != in.ContextKey {
			return 0, errors.New("audit 与当前 claim/context/observation 版本不匹配")
		}
		if saved.Qualification != fresh.Qualification || (!saved.ExpiresAt.IsZero() && time.Now().After(saved.ExpiresAt)) {
			return 0, errors.New("audit 已过期或来源资格已变化，请重新审计")
		}
		if in.ConclusionKind == "observation" && fresh.Qualification != "ready_for_review" {
			return 0, errors.New("来源不足以登记已捕获观察，请保留原始observation并使用inconclusive结论说明缺项")
		}
		var used bool
		if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel='qualifies')`, s.expID, in.AuditID).Scan(&used); err != nil {
			return 0, err
		}
		if used {
			return 0, errors.New("audit 已绑定结论，请为本次结论重新审计")
		}
		// Explicit assertions of absence or security correctness require a
		// semantic oracle. Current runtime checks cannot supply one.
		if in.ConclusionKind == "negative" || in.ConclusionKind == "security_claim" {
			return 0, errors.New("业务否定或安全判断缺少可判定依据，请保存 inconclusive 结论并保留未知边界")
		}
		payload["conclusion_kind"] = in.ConclusionKind
		payload["context_key"] = in.ContextKey
		payload["observation_ids"] = in.ObservationIDs
		payload["audit_id"] = in.AuditID
		payload["qualification"] = fresh.Qualification
		payload["semantic_status"] = "unknown"
		payload["claim_hash"] = fresh.ClaimHash
	}
	id, err := s.insertEvidenceNode(tx, KindFact, payload, "confirmed", in.Worker, in.IntentID, in.AssetIDs)
	if err != nil {
		return 0, err
	}
	for _, obs := range in.ObservationIDs {
		if err = s.linkEvidence(tx, obs, RelSupports, id); err != nil {
			return 0, err
		}
	}
	if in.AuditID > 0 {
		if err = s.linkEvidence(tx, in.AuditID, RelQualifies, id); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}
