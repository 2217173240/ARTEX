import type { TaskNode } from "@/lib/types";

export function evidencePayload(node: TaskNode): Record<string, unknown> | null {
  if (!node.payload?.trim()) return null;
  try {
    const payload: unknown = JSON.parse(node.payload);
    return payload !== null && typeof payload === "object" && !Array.isArray(payload)
      ? (payload as Record<string, unknown>)
      : null;
  } catch {
    // 旧节点允许纯文本，原始内容由节点详情保留。
    return null;
  }
}

export function evidenceObservationIds(payload: Record<string, unknown>): string[] {
  if (!Array.isArray(payload.observation_ids)) return [];
  return Array.from(
    new Set(
      payload.observation_ids
        .filter((id): id is string | number => typeof id === "string" || typeof id === "number")
        .map(String),
    ),
  );
}

export const qualificationLabels: Record<string, string> = {
  ready_for_review: "可供审核",
  insufficient: "证据不足",
  unknown: "尚无法判断",
  legacy_unchecked: "历史记录，尚未核验",
};

export const sourceKindLabels: Record<string, string> = {
  model_recorded: "模型记录",
  activity: "活动记录",
  traffic_snapshot: "流量快照",
};
