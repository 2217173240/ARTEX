package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	actool "github.com/Autumn-27/norma/tool"
)

// These single-record tools use explicit task ownership and the same write-tool
// permission classification as the UI's finding mutations.
func (s *Server) findingMutationTools() []actool.CoreTool {
	props := map[string]any{"task_id": idParam("当前来源任务 ID；继承记录需进入来源任务"), "finding_id": idParam("独立 findings 记录 ID，不是 finding_node_id 或探索节点 ID"), "severity": strParam("critical|high|medium|low"), "status": strParam("pending|in_progress|confirmed|resolved|fixed|false_positive|ignored|duplicate|risk_accepted")}
	out := []actool.CoreTool{}
	for _, name := range []string{"update_finding_record", "delete_finding_record"} {
		name := name
		schemaProps := map[string]any{"task_id": props["task_id"], "finding_id": props["finding_id"]}
		desc := "删除当前来源任务的一条已落库漏洞及其探索节点，使用独立 finding_id。"
		if name == "update_finding_record" {
			schemaProps["severity"] = props["severity"]
			schemaProps["status"] = props["status"]
			desc = "修改当前来源任务的一条已落库漏洞的等级或处置状态；保持报告和证据。使用独立 finding_id。"
		}
		out = append(out, wrTool(name, desc, objSchema(schemaProps, "task_id", "finding_id"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				TaskID    json.RawMessage `json:"task_id"`
				FindingID json.RawMessage `json:"finding_id"`
				Severity  *string         `json:"severity"`
				Status    *string         `json:"status"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			taskID, fid := parseProfileID(a.TaskID), parseProfileID(a.FindingID)
			if taskID <= 0 || fid <= 0 {
				return actool.Errorf("task_id 与独立 finding_id 必填"), nil
			}
			if contextTask := intercept.TaskIDFromContext(ctx); contextTask != "" && contextTask != i64s(taskID) {
				return actool.Errorf("当前任务上下文不允许修改其他来源任务的漏洞；继承记录只读"), nil
			}
			if s.m.ResolveTask(i64s(taskID)) == nil {
				return actool.Errorf("当前任务不可用"), nil
			}
			f, err := s.m.pg.GetFinding(fid)
			if err == nil && (f == nil || f.TaskID == nil || *f.TaskID != taskID) {
				err = errors.New("只能修改当前来源任务的漏洞；继承记录只读")
			}
			if err == nil {
				err = s.checkCaseReviewScope(ctx, name, findingCaseRequest{FindingID: a.FindingID})
			}
			if err == nil {
				if name == "delete_finding_record" {
					var n int64
					n, err = s.m.pg.DeleteFindingInTask(ctx, taskID, fid)
					if err == nil && n == 0 {
						err = db.ErrFindingNotFound
					}
				} else {
					err = s.m.pg.MutateFindingRecord(ctx, taskID, fid, a.Severity, a.Status)
				}
			}
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			action := "updated"
			if name == "delete_finding_record" {
				action = "deleted"
			}
			return actool.Text("finding " + i64s(fid) + " " + action), nil
		}))
	}
	return out
}
