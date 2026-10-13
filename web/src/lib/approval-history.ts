import type { InterceptApprovalFilter, InterceptAudit, InterceptPending } from "./types";

// Final human decisions can be proven for older snapshots even when the
// persisted source still describes the initial rule/model review.
export function approvalDecisionSource(row: InterceptPending, audit?: Pick<InterceptAudit, "decision_reason"> | null) {
  if (
    (row.status === "allowed" && audit?.decision_reason === "人工允许执行") ||
    (row.status === "denied" && audit?.decision_reason === "人工拒绝执行")
  )
    return "manual";
  if (row.decision_source) return row.decision_source;
  if (row.rule_id) return "rule";
  return row.reason.startsWith("[模型]") ? "model" : "unknown";
}

export function approvalFilterError(filter: InterceptApprovalFilter): string | null {
  const q = filter.q?.trim() ?? "";
  if ([...q].length > 200) return "搜索关键词不能超过 200 个字符";
  if (q.startsWith("#") && (!/^#[1-9][0-9]*$/.test(q) || BigInt(q.slice(1)) > BigInt("9223372036854775807")))
    return "审批编号必须为 # 后接正整数，且不能超过 9223372036854775807";
  for (const value of [filter.created_from, filter.created_to]) {
    if (!value) continue;
    const instant = new Date(`${value}T00:00:00Z`);
    if (
      !/^\d{4}-\d{2}-\d{2}$/.test(value) ||
      !Number.isFinite(instant.getTime()) ||
      instant.toISOString().slice(0, 10) !== value
    )
      return "日期必须是有效的 YYYY-MM-DD 日期";
  }
  if (filter.created_from && filter.created_to && filter.created_from > filter.created_to)
    return "开始日期不能晚于结束日期";
  return null;
}
