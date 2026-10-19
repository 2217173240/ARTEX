package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func evidenceIDs(raw []json.RawMessage) ([]int64, error) {
	ids := make([]int64, 0, len(raw))
	for _, value := range raw {
		id := pid(value)
		if id <= 0 {
			return nil, fmt.Errorf("observation_id 必须是有效的正整数")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (t *ToolSet) evidenceIntent(explicit json.RawMessage) int64 {
	if len(explicit) > 0 {
		return pid(explicit)
	}
	return t.ownerNode
}

func (t *ToolSet) recordObservation() actool.CoreTool {
	return t.writeExpTool("record_observation", "保存本意图的原始观察。source_kind=activity 引用已经保存的工具结果；仅实际执行回执标记 captured，旧记录保留未核验标记。只保证保存已有工具输出字节，输出可能截断，不能宣称完整HTTP响应。traffic_snapshot 仅复用已绑定本任务漏洞的持久快照ID。省略source_kind固定为model_recorded，模型填写内容不能冒充捕获来源。context_key明确记录目标、身份、对象和用途，后续结论须保持一致。不会发出网络请求。", obj(map[string]any{
		"intent_id": idp("本任务意图ID；worker省略时使用当前意图"), "summary": str("观察摘要"), "detail": str("模型说明，与原始来源分开保存"), "context_key": str("目标/身份/对象/用途上下文"), "source_kind": map[string]any{"type": "string", "enum": []string{"model_recorded", "activity", "traffic_snapshot"}}, "source_id": str("已有activity整数ID或CAS snapshot ID；model_recorded省略"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
	}, "summary", "context_key"), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
		var a struct {
			db.ObservationInput
			IntentID json.RawMessage   `json:"intent_id"`
			AssetIDs []json.RawMessage `json:"asset_ids"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		a.ObservationInput.IntentID = t.evidenceIntent(a.IntentID)
		a.ObservationInput.AssetIDs = pidList(a.AssetIDs)
		a.TaskID = t.taskID
		a.Worker = t.worker
		n, err := t.ts.RecordObservation(ctx, a.ObservationInput)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		t.writes.Observations++
		return jsonResult(n)
	})
}

func (t *ToolSet) auditFactValidity() actool.CoreTool {
	return t.writeExpTool("audit_fact_validity", "审计特定结论与观察已保存来源记录/元数据的完整性、同任务/意图/上下文声明、新鲜度和版本。CAS正文当前可用性在读取时核验，本审计不读取正文。summary/detail/conclusion_kind/context_key/observation_ids须与后续record_fact完全一致，返回audit_id为节点id。ready_for_review仅表示保存来源满足机器检查；业务语义仍unknown。推断/否定/安全判断不能从响应差异自动确认。", obj(map[string]any{
		"intent_id": idp("本任务意图ID；省略使用当前意图"), "summary": str("后续结论的准确摘要"), "detail": str("后续结论的准确详情"), "conclusion_kind": map[string]any{"type": "string", "enum": []string{"observation", "inference", "negative", "inconclusive", "security_claim"}}, "context_key": str("与观察一致的目标/身份/对象/用途"), "observation_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
	}, "summary", "conclusion_kind", "context_key", "observation_ids"), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
		var a struct {
			db.QualificationInput
			IntentID       json.RawMessage   `json:"intent_id"`
			ObservationIDs []json.RawMessage `json:"observation_ids"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		a.QualificationInput.IntentID = t.evidenceIntent(a.IntentID)
		ids, err := evidenceIDs(a.ObservationIDs)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		a.QualificationInput.ObservationIDs = ids
		a.TaskID = t.taskID
		a.Worker = t.worker
		n, err := t.ts.AuditFactValidity(ctx, a.QualificationInput)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		t.writes.Qualifications++
		return jsonResult(n)
	})
}

func (t *ToolSet) listObservations() actool.CoreTool {
	return t.readExpTool("list_observations", "检索本任务 observation/validity_gate/fact；原始来源及资格详情使用node_detail或节点详情接口。", obj(map[string]any{"kind": str("observation/validity_gate/fact，省略表示全部"), "q": str("内容关键字"), "before": idp("分页游标"), "limit": intp("最多200条")}), func(_ context.Context, raw json.RawMessage) (actool.Result, error) {
		var a struct {
			Kind, Q string
			Before  json.RawMessage
			Limit   int
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		v, err := t.ts.ListEvidence(a.Kind, a.Q, a.Limit, pid(a.Before))
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		return jsonResult(v)
	})
}
