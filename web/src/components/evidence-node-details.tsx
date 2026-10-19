"use client";

import { ObservationTrafficBody } from "@/components/observation-traffic-body";
import { Badge } from "@/components/ui/badge";
import { evidenceObservationIds, evidencePayload, qualificationLabels, sourceKindLabels } from "@/lib/evidence-chain";
import { useI18n } from "@/lib/i18n";
import type { TaskNode } from "@/lib/types";

export function EvidenceNodeDetails({
  node,
  nodesById,
  onSelect,
  onSelectId,
  taskId,
}: {
  node: TaskNode;
  nodesById: Map<string, TaskNode>;
  onSelect: (node: TaskNode) => void;
  onSelectId?: (id: string) => void;
  taskId?: string;
}) {
  const { t: uiText } = useI18n();
  if (!["observation", "validity_gate", "fact"].includes(node.type) || node.state === "origin") return null;
  const payload = evidencePayload(node);
  if (!payload) return null;
  const text = (key: string) => (typeof payload[key] === "string" ? (payload[key] as string) : "");
  const ids = evidenceObservationIds(payload);
  const qualification = text("qualification") || (node.type === "fact" ? "legacy_unchecked" : "");
  const snapshot = payload.source_snapshot;
  const reasons = Array.isArray(payload.reasons)
    ? payload.reasons.filter((reason): reason is string => typeof reason === "string")
    : [];

  return (
    <section className="space-y-3 border-t pt-3">
      <h4 className="font-medium text-sm">{uiText("结构化证据")}</h4>
      {qualification && (
        <div className="space-y-2">
          <Badge variant={qualification === "ready_for_review" ? "secondary" : "outline"}>
            {uiText(qualificationLabels[qualification] ?? qualification)}
          </Badge>
          <p className="text-muted-foreground text-xs">
            {uiText("资格表示证据结构的完整性，语义与漏洞结论仍需审核。")}
          </p>
        </div>
      )}
      {text("summary") && <p className="whitespace-pre-wrap break-words text-sm">{text("summary")}</p>}
      {text("detail") && (
        <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted/50 p-2 text-xs">
          {text("detail")}
        </pre>
      )}
      <dl className="space-y-2 text-xs">
        {(
          [
            [
              "来源类型",
              text("source_kind") ? uiText(sourceKindLabels[text("source_kind")] ?? text("source_kind")) : "",
            ],
            ["来源记录", payload.source_id],
            ["来源时间", text("source_time")],
            ["来源摘要哈希", text("source_hash")],
            ["上下文标识", text("context_key")],
            [
              "结论类型",
              text("conclusion_kind")
                ? uiText(
                    (
                      {
                        observation: "观察性结论",
                        inconclusive: "结论不明确",
                        negative: "业务否定",
                        security_claim: "安全判断",
                      } as Record<string, string>
                    )[text("conclusion_kind")] ?? text("conclusion_kind"),
                  )
                : "",
            ],
            ["结论摘要哈希", text("claim_hash")],
            ["核验时间", text("checked_at")],
            ["资格记录", payload.audit_id],
            [
              "采集状态",
              text("capture_status")
                ? uiText(
                    (
                      {
                        captured: "运行时采集",
                        legacy_unverified: "历史来源，采集未核验",
                        model_recorded: "模型记录，缺少独立采集",
                      } as Record<string, string>
                    )[text("capture_status")] ?? text("capture_status"),
                  )
                : "",
            ],
            ["语义判定", text("semantic_status") === "unknown" ? uiText("尚无法判断") : text("semantic_status")],
            [
              "上下文保证",
              text("context_assurance") === "declared"
                ? uiText("声明的上下文，未经独立验证")
                : text("context_assurance"),
            ],
            [
              "完整性范围",
              text("integrity_scope") === "saved_source_record" ? uiText("已保存的来源记录") : text("integrity_scope"),
            ],
            ["核验有效期至", text("expires_at")],
            [
              "来源范围",
              text("source_scope")
                ? uiText(
                    (
                      {
                        model_text: "模型文本",
                        saved_tool_result: "已保存的工具结果",
                        persistent_traffic_snapshot: "持久化流量快照",
                      } as Record<string, string>
                    )[text("source_scope")] ?? text("source_scope"),
                  )
                : "",
            ],
          ] as const
        )
          .filter(([, value]) => value !== undefined && value !== null && value !== "")
          .map(([label, value]) => (
            <div key={label} className="space-y-1">
              <dt className="font-medium text-muted-foreground">{uiText(label)}</dt>
              <dd className="break-all">{String(value)}</dd>
            </div>
          ))}
      </dl>
      {reasons.length > 0 && (
        <div className="space-y-1">
          <h5 className="font-medium text-xs">{uiText("核验说明")}</h5>
          <ul className="list-disc space-y-1 pl-4 text-xs">
            {Array.from(new Set(reasons)).map((reason) => (
              <li key={reason} className="whitespace-pre-wrap break-words">
                {reason}
              </li>
            ))}
          </ul>
        </div>
      )}
      {ids.length > 0 && (
        <div className="space-y-2">
          <h5 className="font-medium text-xs">{uiText("关联观察记录")}</h5>
          {ids.map((id) => {
            const observation = nodesById.get(id);
            if (observation)
              return (
                <button
                  key={id}
                  type="button"
                  onClick={() => onSelect(observation)}
                  className="block w-full rounded-md border p-2 text-left text-xs hover:bg-accent focus-visible:outline focus-visible:outline-ring"
                >
                  <span className="font-mono">#{id}</span>
                  <span className="ml-2 break-words">
                    {String(evidencePayload(observation)?.summary ?? uiText("观察记录"))}
                  </span>
                </button>
              );
            if (onSelectId)
              return (
                <button
                  key={id}
                  type="button"
                  onClick={() => onSelectId(id)}
                  className="block w-full rounded-md border p-2 text-left text-xs hover:bg-accent focus-visible:outline focus-visible:outline-ring"
                >
                  #{id} · {uiText("展开观察记录")}
                </button>
              );
            return (
              <p key={id} className="text-muted-foreground text-xs">
                #{id} · {uiText("该记录未随当前图加载")}
              </p>
            );
          })}
        </div>
      )}
      {(typeof payload.audit_id === "string" || typeof payload.audit_id === "number") &&
        (nodesById.has(String(payload.audit_id)) || onSelectId) && (
          <button
            type="button"
            className="block w-full rounded-md border p-2 text-left text-xs hover:bg-accent focus-visible:outline focus-visible:outline-ring"
            onClick={() => {
              const gate = nodesById.get(String(payload.audit_id));
              if (gate) onSelect(gate);
              else onSelectId?.(String(payload.audit_id));
            }}
          >
            {uiText("展开资格核验")} · #{String(payload.audit_id)}
          </button>
        )}
      {snapshot !== undefined && snapshot !== null && (
        <details className="rounded-md border p-2 text-xs">
          <summary className="cursor-pointer font-medium">{uiText("来源快照")}</summary>
          <pre className="mt-2 max-h-80 overflow-auto whitespace-pre-wrap break-words">
            {typeof snapshot === "string" ? snapshot : JSON.stringify(snapshot, null, 2)}
          </pre>
        </details>
      )}
      {node.type === "observation" && text("source_kind") === "traffic_snapshot" && taskId && (
        <ObservationTrafficBody
          taskId={node.source_task_id?.trim() ? node.source_task_id : taskId}
          nodeId={node.id}
          snapshot={snapshot}
        />
      )}
    </section>
  );
}
