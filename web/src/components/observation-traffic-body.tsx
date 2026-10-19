"use client";

import * as React from "react";

import { HttpCodeBlock } from "@/components/http-code-block";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import type { EvidenceBodyPreview } from "@/lib/types";

function BodySide({
  taskId,
  nodeId,
  side,
  header,
}: {
  taskId: string;
  nodeId: string;
  side: "request" | "response";
  header: string;
}) {
  const { t: uiText } = useI18n();
  const [body, setBody] = React.useState<EvidenceBodyPreview | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");
  const version = React.useRef(0);
  React.useEffect(
    () => () => {
      version.current++;
    },
    [],
  );
  const load = async () => {
    const requestVersion = ++version.current;
    setBusy(true);
    setError("");
    try {
      const data = await api.taskEvidenceBody(taskId, nodeId, side, body?.next_offset ?? 0);
      if (requestVersion === version.current)
        setBody((current) => ({ ...data, content: (current?.content ?? "") + data.content }));
    } catch (reason) {
      if (requestVersion === version.current) setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      if (requestVersion === version.current) setBusy(false);
    }
  };
  const download = async () => {
    setError("");
    try {
      await api.downloadTaskEvidenceBody(taskId, nodeId, side);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    }
  };
  return (
    <div className="space-y-2 rounded-md border p-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h5 className="font-medium text-xs">{side === "request" ? uiText("请求正文") : uiText("响应正文")}</h5>
        <Button size="sm" variant="outline" onClick={() => void download()}>
          {uiText("下载完整正文")}
        </Button>
      </div>
      {error && (
        <p role="alert" className="break-words text-destructive text-xs">
          {error}
        </p>
      )}
      {body && (
        <>
          <p className="text-muted-foreground text-xs">
            {body.total.toLocaleString()} {uiText("字节")}
            {body.truncated ? uiText("· 当前为预览") : ""}
          </p>
          {body.binary ? (
            <p className="text-muted-foreground text-xs">{uiText("二进制正文，可下载原始文件。")}</p>
          ) : (
            <HttpCodeBlock raw={`${header.trimEnd()}\n\n${body.content}`} />
          )}
        </>
      )}
      {(!body || (body.truncated && !body.binary)) && (
        <Button size="sm" variant="outline" disabled={busy} onClick={() => void load()}>
          {busy ? uiText("加载中…") : uiText(body ? "加载更多正文" : "预览正文")}
        </Button>
      )}
    </div>
  );
}

export function ObservationTrafficBody({
  taskId,
  nodeId,
  snapshot,
}: {
  taskId: string;
  nodeId: string;
  snapshot: unknown;
}) {
  const source = snapshot && typeof snapshot === "object" ? (snapshot as Record<string, unknown>) : {};
  return (
    <div className="space-y-2">
      {(["request", "response"] as const).map((side) => (
        <BodySide
          key={`${taskId}:${nodeId}:${side}`}
          taskId={taskId}
          nodeId={nodeId}
          side={side}
          header={
            typeof source[side === "request" ? "req_head" : "resp_head"] === "string"
              ? (source[side === "request" ? "req_head" : "resp_head"] as string)
              : ""
          }
        />
      ))}
    </div>
  );
}
