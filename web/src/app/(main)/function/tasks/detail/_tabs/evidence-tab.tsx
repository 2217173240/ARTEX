"use client";

import * as React from "react";

import { RefreshCwIcon, SearchIcon } from "lucide-react";

import { EvidenceNodeDetails } from "@/components/evidence-node-details";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api } from "@/lib/api";
import { evidencePayload } from "@/lib/evidence-chain";
import { useI18n } from "@/lib/i18n";
import type { TaskEvidenceDetail, TaskNode } from "@/lib/types";

const kindLabels: Record<string, string> = { observation: "观察记录", validity_gate: "证据资格核验", fact: "事实" };

export function EvidenceTab({ taskId }: { taskId: string }) {
  const { t: uiText, locale } = useI18n();
  const [kind, setKind] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [filter, setFilter] = React.useState({ kind: "", q: "" });
  const [nodes, setNodes] = React.useState<TaskNode[]>([]);
  const [nextBefore, setNextBefore] = React.useState<number>();
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  const [detail, setDetail] = React.useState<TaskEvidenceDetail | null>(null);
  const [selected, setSelected] = React.useState<TaskNode | null>(null);
  const [detailLoading, setDetailLoading] = React.useState(false);
  const [detailError, setDetailError] = React.useState("");
  const [detailHistory, setDetailHistory] = React.useState<{ taskId: string; nodeId: string }[]>([]);
  const listVersion = React.useRef(0);
  const detailVersion = React.useRef(0);
  const detailRequest = React.useRef({ taskId, nodeId: "" });

  const load = React.useCallback(
    async (before?: number) => {
      const version = ++listVersion.current;
      setLoading(true);
      setError("");
      try {
        const data = await api.taskEvidence(taskId, { ...filter, before });
        if (version !== listVersion.current) return;
        setNodes((current) =>
          before
            ? [...current, ...data.nodes.filter((node) => !current.some((existing) => existing.id === node.id))]
            : data.nodes,
        );
        setNextBefore(data.next_before);
      } catch (reason) {
        if (version === listVersion.current) setError(reason instanceof Error ? reason.message : String(reason));
      } finally {
        if (version === listVersion.current) setLoading(false);
      }
    },
    [taskId, filter],
  );

  React.useEffect(() => {
    setNodes([]);
    setNextBefore(undefined);
    void load();
    return () => {
      listVersion.current++;
    };
  }, [load]);

  React.useEffect(
    () => () => {
      detailVersion.current++;
    },
    [],
  );

  const loadDetail = async (nodeId: string, ownerTaskId: string) => {
    const version = ++detailVersion.current;
    detailRequest.current = { taskId: ownerTaskId, nodeId };
    setDetail(null);
    setDetailError("");
    setDetailLoading(true);
    try {
      const data = await api.taskEvidenceDetail(ownerTaskId, nodeId);
      if (version === detailVersion.current) {
        setDetail(data);
        setSelected(data.node);
      }
    } catch (reason) {
      if (version === detailVersion.current) setDetailError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      if (version === detailVersion.current) setDetailLoading(false);
    }
  };
  const selectNode = (node: TaskNode) => {
    setDetailHistory([]);
    setSelected(node);
    void loadDetail(node.id, node.source_task_id?.trim() ? node.source_task_id : taskId);
  };
  const followRecord = (nodeId: string, ownerTaskId = detailRequest.current.taskId) => {
    setDetailHistory((history) => [...history, detailRequest.current]);
    void loadDetail(nodeId, ownerTaskId);
  };
  const returnToRecord = () => {
    const previous = detailHistory.at(-1);
    if (!previous) return;
    setDetailHistory((history) => history.slice(0, -1));
    void loadDetail(previous.nodeId, previous.taskId);
  };
  const currentNode = detail?.node ?? selected;
  const nodesById = new Map((detail?.nodes ?? []).map((node) => [node.id, node]));

  return (
    <>
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between gap-2">
            <CardTitle className="text-base">{uiText("证据记录")}</CardTitle>
            <Button size="sm" variant="outline" disabled={loading} onClick={() => void load()}>
              <RefreshCwIcon className={loading ? "size-3.5 animate-spin" : "size-3.5"} />
              {uiText("刷新")}
            </Button>
          </div>
          <CardDescription>
            {uiText(
              "查询当前任务的观察、资格核验和事实，展开查看来源快照与关联记录。继承任务的记录可在探索链路中查看。",
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <form
            className="flex flex-wrap items-center gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              setFilter({ kind, q: query.trim() });
            }}
          >
            <NativeSelect
              value={kind}
              onChange={(event) => setKind(event.target.value)}
              aria-label={uiText("证据类型")}
            >
              <NativeSelectOption value="">{uiText("全部类型")}</NativeSelectOption>
              {Object.entries(kindLabels).map(([value, label]) => (
                <NativeSelectOption key={value} value={value}>
                  {uiText(label)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <Input
              className="min-w-48 flex-1"
              value={query}
              maxLength={500}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={uiText("搜索摘要、来源或上下文标识")}
              aria-label={uiText("搜索证据记录")}
            />
            <Button type="submit" variant="outline">
              <SearchIcon className="size-3.5" />
              {uiText("查询")}
            </Button>
          </form>
          {error && (
            <div role="alert" className="space-y-1 text-destructive text-sm">
              <p>{uiText("证据记录暂时不可用，请重试；冷归档任务需先还原。")}</p>
              <p className="break-words text-xs">{error}</p>
            </div>
          )}
          {!error && nodes.length === 0 && (
            <p role="status" className="text-muted-foreground text-sm">
              {loading ? uiText("加载中…") : uiText("当前筛选下没有证据记录。")}
            </p>
          )}
          <div className="space-y-2" aria-busy={loading}>
            {nodes.map((node) => (
              <button
                key={node.id}
                type="button"
                onClick={() => void selectNode(node)}
                className="flex w-full flex-wrap items-start gap-3 rounded-md border p-3 text-left hover:bg-accent focus-visible:outline focus-visible:outline-ring"
              >
                <Badge variant="outline">{uiText(kindLabels[node.type] ?? node.type)}</Badge>
                <span className="min-w-0 flex-1 space-y-1">
                  <span className="block whitespace-pre-wrap break-words text-sm">
                    {String(evidencePayload(node)?.summary ?? node.payload ?? "—")}
                  </span>
                  <span className="block text-muted-foreground text-xs">
                    #{node.id} · {new Date(node.ts).toLocaleString(locale)}
                  </span>
                </span>
              </button>
            ))}
          </div>
          {nextBefore && (
            <Button variant="outline" disabled={loading} onClick={() => void load(nextBefore)}>
              {loading ? uiText("加载中…") : uiText("加载更多")}
            </Button>
          )}
        </CardContent>
      </Card>
      <Sheet
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) {
            detailVersion.current++;
            setSelected(null);
            setDetail(null);
            setDetailHistory([]);
          }
        }}
      >
        <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
          <SheetHeader>
            <SheetTitle>
              {uiText(kindLabels[currentNode?.type ?? ""] ?? "证据记录")} #{currentNode?.id}
            </SheetTitle>
            <SheetDescription>{uiText("来源快照和核验记录保持原文，可在此展开关联记录。")}</SheetDescription>
          </SheetHeader>
          <div className="space-y-4 px-4 pb-4">
            {detailHistory.length > 0 && (
              <Button size="sm" variant="outline" disabled={detailLoading} onClick={returnToRecord}>
                {uiText("返回上一条记录")}
              </Button>
            )}
            {detailLoading && (
              <p role="status" className="text-muted-foreground text-sm">
                {uiText("加载中…")}
              </p>
            )}
            {detailError && (
              <div role="alert" className="space-y-2 text-destructive text-sm">
                <p>{detailError}</p>
                {selected && (
                  <Button
                    variant="outline"
                    onClick={() => void loadDetail(detailRequest.current.nodeId, detailRequest.current.taskId)}
                  >
                    {uiText("重试")}
                  </Button>
                )}
              </div>
            )}
            {detail && (
              <EvidenceNodeDetails
                node={detail.node}
                taskId={detailRequest.current.taskId}
                nodesById={nodesById}
                onSelect={(node) =>
                  followRecord(
                    node.id,
                    node.source_task_id?.trim() ? node.source_task_id : detailRequest.current.taskId,
                  )
                }
                onSelectId={followRecord}
              />
            )}
            {detail && (
              <details className="rounded-md border p-3 text-xs">
                <summary className="cursor-pointer font-medium">{uiText("原始数据")}</summary>
                <pre className="mt-2 overflow-auto whitespace-pre-wrap break-words">
                  {JSON.stringify(detail, null, 2)}
                </pre>
              </details>
            )}
          </div>
        </SheetContent>
      </Sheet>
    </>
  );
}
