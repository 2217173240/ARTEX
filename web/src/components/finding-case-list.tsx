"use client";

import * as React from "react";

import Link from "next/link";

import { ChevronRightIcon, FolderIcon, FolderOpenIcon } from "lucide-react";
import { toast } from "sonner";

import { StatusBadge } from "@/components/status-badge";
import { TablePagination } from "@/components/table-pagination";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import type {
  Finding,
  FindingCase,
  FindingCasePage,
  FindingCaseReviewRun,
  FindingCaseSuggestion,
  FindingQuery,
} from "@/lib/types";
import { cn } from "@/lib/utils";

export function FindingSeverityCounts({
  counts,
}: {
  counts: Pick<FindingCase, "critical" | "high" | "medium" | "low">;
}) {
  const { t: uiText } = useI18n();
  return (
    <span
      className="inline-flex items-center gap-0.5 text-sm tabular-nums"
      title={uiText("原始上报记录：严重 / 高 / 中 / 低")}
      role="img"
      aria-label={uiText("严重{v0}，高危{v1}，中危{v2}，低危{v3}", {
        v0: counts.critical,
        v1: counts.high,
        v2: counts.medium,
        v3: counts.low,
      })}
    >
      <span className={counts.critical ? "text-severity-critical" : "text-muted-foreground"}>{counts.critical}</span>
      <span>/</span>
      <span className={counts.high ? "text-severity-high" : "text-muted-foreground"}>{counts.high}</span>
      <span>/</span>
      <span className={counts.medium ? "text-severity-medium" : "text-muted-foreground"}>{counts.medium}</span>
      <span>/</span>
      <span className={counts.low ? "text-severity-low" : "text-muted-foreground"}>{counts.low}</span>
    </span>
  );
}

export function FindingCaseMemberRow({
  finding: f,
  selected,
  onSelect,
  contextTask,
  matched = true,
  nested = false,
}: {
  finding: Finding;
  selected?: boolean;
  onSelect?: (id: string, checked: boolean) => void;
  contextTask?: string;
  matched?: boolean;
  nested?: boolean;
}) {
  const { t: uiText } = useI18n();
  const id = f.finding_id ?? f.id;
  return (
    <div
      className={cn(
        "flex min-w-0 flex-wrap items-center gap-3 py-3 text-sm",
        nested
          ? "relative rounded-lg border bg-background px-3 before:absolute before:top-1/2 before:-left-4 before:w-4 before:border-border before:border-t sm:before:-left-5 sm:before:w-5"
          : "border-b px-4 last:border-b-0",
        !matched && "bg-muted/30",
      )}
    >
      {onSelect && matched && !f.inherited ? (
        <Checkbox
          checked={selected}
          onCheckedChange={(v) => onSelect(id, v === true)}
          aria-label={uiText("选择上报 #{v0}", { v0: id })}
        />
      ) : null}
      <StatusBadge domain="severity" value={f.severity} />
      <div className="order-last flex min-w-0 flex-1 basis-full flex-col gap-1 sm:order-none sm:basis-auto">
        <Link
          className="line-clamp-2 break-words font-medium hover:underline sm:truncate"
          href={`/function/findings/detail?id=${id}${contextTask ? `&context_task=${contextTask}` : ""}`}
        >
          #{id} {[f.name, f.vulnclass].find((v) => v?.trim()) ?? uiText("未分类")}
        </Link>
        <span className="line-clamp-2 break-words text-muted-foreground text-xs sm:truncate">{f.summary}</span>
      </div>
      {!matched ? <Badge variant="outline">{uiText("未命中当前筛选")}</Badge> : null}
      {f.inherited ? <Badge variant="outline">{uiText("继承 · 只读")}</Badge> : null}
      <StatusBadge domain="finding" value={f.status} />
      <Button asChild variant="ghost" size="sm">
        <Link href={`/function/findings/detail?id=${id}${contextTask ? `&context_task=${contextTask}` : ""}`}>
          {uiText("查看报告")}
        </Link>
      </Button>
    </div>
  );
}

export function FindingCaseMembers({
  caseId,
  version,
  matchedIds,
  selectedIds,
  onSelect,
  contextTask,
  nested = false,
  renderRecords,
  refreshToken = 0,
}: {
  caseId: string;
  version?: number;
  matchedIds?: number[];
  selectedIds?: Set<string>;
  onSelect?: (id: string, checked: boolean) => void;
  contextTask?: string;
  nested?: boolean;
  renderRecords?: (items: Finding[]) => React.ReactNode;
  refreshToken?: number;
}) {
  const { t: uiText } = useI18n();
  const [page, setPage] = React.useState(1);
  const [snapshot, setSnapshot] = React.useState<{ key: string; value: { items: Finding[]; total: number } } | null>(
    null,
  );
  const requestKey = JSON.stringify([caseId, page, contextTask]);
  const data = snapshot?.key === requestKey ? snapshot.value : null;
  const [failure, setFailure] = React.useState<{ key: string; message: string } | null>(null);
  const [retry, setRetry] = React.useState(0);
  const error = failure?.key === requestKey ? failure.message : "";
  // biome-ignore lint/correctness/useExhaustiveDependencies: changing case context resets independent member pagination.
  React.useEffect(() => {
    setPage(1);
  }, [caseId, contextTask]);
  React.useEffect(() => {
    if (data && page > Math.max(1, Math.ceil(data.total / 20))) setPage(Math.max(1, Math.ceil(data.total / 20)));
  }, [data, page]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: version and retry deliberately reload changed evidence.
  React.useEffect(() => {
    let active = true;
    setFailure(null);
    api
      .findingCaseMembers(caseId, page, contextTask)
      .then((v) => {
        if (active) setSnapshot({ key: requestKey, value: v });
      })
      .catch((e) => {
        if (active) setFailure({ key: requestKey, message: (e as Error).message });
      });
    return () => {
      active = false;
    };
  }, [caseId, page, version, contextTask, retry, requestKey, refreshToken]);
  if (error && !data)
    return (
      <Alert>
        <AlertDescription>
          {uiText("成员加载失败：")}
          {error}
          <Button variant="link" onClick={() => setRetry((v) => v + 1)}>
            {uiText("重试")}
          </Button>
        </AlertDescription>
      </Alert>
    );
  if (!data)
    return (
      <div className="flex justify-center p-5">
        <Spinner />
      </div>
    );
  return (
    <div className={cn("flex min-w-0 flex-col", nested && "gap-2 border-primary/25 border-l-2 pl-4 sm:pl-5")}>
      {error ? (
        <Alert>
          <AlertDescription>
            {uiText("成员加载失败：")}
            {error}
            <Button variant="link" onClick={() => setRetry((v) => v + 1)}>
              {uiText("重试")}
            </Button>
          </AlertDescription>
        </Alert>
      ) : null}
      {renderRecords
        ? renderRecords(data.items)
        : data.items.map((f) => (
            <FindingCaseMemberRow
              key={f.finding_id ?? f.id}
              finding={f}
              nested={nested}
              selected={selectedIds?.has(f.finding_id ?? f.id)}
              onSelect={onSelect}
              contextTask={contextTask}
              matched={!matchedIds || matchedIds.includes(Number(f.finding_id ?? f.id))}
            />
          ))}
      {data.total > 20 ? (
        <TablePagination
          page={page}
          pageSize={20}
          total={data.total}
          onPageChange={setPage}
          onPageSizeChange={() => setPage(1)}
          pageSizeOptions={[20]}
        />
      ) : null}
    </div>
  );
}

function FindingMergeSuggestionCard({
  suggestion: s,
  onResolve,
}: {
  suggestion: FindingCaseSuggestion;
  onResolve: (s: FindingCaseSuggestion, accept: boolean) => Promise<void>;
}) {
  const { t: uiText } = useI18n();
  const [expanded, setExpanded] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  async function resolve(accept: boolean) {
    if (busy) return;
    setBusy(true);
    try {
      await onResolve(s, accept);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Card size="sm" className="min-w-0">
      <CardHeader>
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant="outline">{uiText("疑似重复 · 待确认")}</Badge>
          <CardDescription>
            {uiText("任务 #")}
            {s.task_id}
          </CardDescription>
        </div>
        <CardTitle className="break-words">{s.title}</CardTitle>
        <CardDescription className="flex flex-wrap gap-3">
          <Link className="underline underline-offset-4" href={`/function/findings/detail/?id=${s.left_id}`}>
            {uiText("查看原报告 #")}
            {s.left_id}
          </Link>
          <Link className="underline underline-offset-4" href={`/function/findings/detail/?id=${s.right_id}`}>
            {uiText("查看原报告 #")}
            {s.right_id}
          </Link>
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Collapsible open={expanded} onOpenChange={setExpanded}>
          {!expanded ? (
            <p className="line-clamp-2 whitespace-pre-wrap break-words text-muted-foreground text-sm">{s.reason}</p>
          ) : null}
          <CollapsibleContent className="whitespace-pre-wrap break-words text-muted-foreground text-sm">
            {s.reason}
          </CollapsibleContent>
          <CollapsibleTrigger asChild>
            <Button variant="link" size="sm">
              {expanded ? uiText("收起判断依据") : uiText("展开判断依据")}
            </Button>
          </CollapsibleTrigger>
        </Collapsible>
      </CardContent>
      <CardFooter className="flex flex-wrap gap-2">
        <Button variant="outline" size="sm" disabled={busy} onClick={() => resolve(true)}>
          {busy ? <Spinner /> : null}
          {uiText("确认归并")}
        </Button>
        <Button variant="ghost" size="sm" disabled={busy} onClick={() => resolve(false)}>
          {uiText("不是同一漏洞")}
        </Button>
      </CardFooter>
    </Card>
  );
}

function FindingReviewRunRow({ run: r }: { run: FindingCaseReviewRun }) {
  const { t: uiText } = useI18n();
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2 text-sm">
      {r.state === "running" ? <Spinner /> : null}
      <span>
        {uiText("任务 #")}
        {r.task_id} · {uiText("整理 #")}
        {r.conversation_id}
      </span>
      <Badge variant={r.state === "failed" ? "destructive" : "secondary"}>
        {
          { queued: uiText("排队中"), running: uiText("运行中"), done: uiText("已完成"), failed: uiText("失败") }[
            r.state
          ]
        }
      </Badge>
      <Link className="underline underline-offset-4" href={`/chat?c=${r.conversation_id}`}>
        {uiText("查看执行记录")}
      </Link>
      {r.error ? <p className="w-full break-words text-destructive">{r.error}</p> : null}
    </div>
  );
}

export function FindingCaseReviewPanel({ taskId, onChange }: { taskId?: string; onChange?: () => void }) {
  const { t: uiText } = useI18n();
  const [reviewSnapshot, setReviewSnapshot] = React.useState<{
    taskId?: string;
    suggestions: FindingCaseSuggestion[];
    runs: FindingCaseReviewRun[];
  } | null>(null);
  const suggestions = reviewSnapshot?.taskId === taskId ? (reviewSnapshot?.suggestions ?? []) : [];
  const runs = reviewSnapshot?.taskId === taskId ? (reviewSnapshot?.runs ?? []) : [];
  const [resolving, setResolving] = React.useState<Set<string>>(() => new Set());
  const [error, setError] = React.useState("");
  const [historyOpen, setHistoryOpen] = React.useState(false);
  React.useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    async function load() {
      try {
        const [s, r] = await Promise.all([api.findingCaseSuggestions(taskId), api.findingCaseReviewRuns()]);
        if (active) {
          setReviewSnapshot({ taskId, suggestions: s, runs: r.filter((x) => !taskId || x.task_id === taskId) });
          setError("");
        }
      } catch (e) {
        if (active) setError((e as Error).message);
      } finally {
        if (active) timer = setTimeout(load, 5000);
      }
    }
    void load();
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [taskId]);
  async function resolve(s: FindingCaseSuggestion, accept: boolean) {
    if (resolving.has(s.id)) return;
    setResolving((current) => new Set([...current, s.id]));
    try {
      const result = await api.resolveFindingCaseSuggestion(s.id, accept);
      setReviewSnapshot((current) =>
        current ? { ...current, suggestions: current.suggestions.filter((x) => x.id !== s.id) } : null,
      );
      if (accept && result.case_id !== "0") {
        toast.success(uiText("已归并，统一报告待生成"));
      } else {
        toast.success(uiText("已否决，不会自动再次归并"));
      }
      onChange?.();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setResolving((current) => new Set([...current].filter((id) => id !== s.id)));
    }
  }
  const latestByTask = new Map<string, FindingCaseReviewRun>();
  for (const run of runs) if (!latestByTask.has(run.task_id)) latestByTask.set(run.task_id, run);
  const latestFailures = [...latestByTask.values()].filter((run) => run.state === "failed");
  return (
    <div className="flex flex-col gap-2">
      {error ? (
        <Alert>
          <AlertDescription>
            {uiText("整理状态加载失败：")}
            {error}
          </AlertDescription>
        </Alert>
      ) : null}
      {suggestions.length > 0 ? (
        <p className="text-muted-foreground text-sm">
          {uiText("待确认归并建议（{count}）· 确认前原始记录分别保留", { count: suggestions.length })}
        </p>
      ) : null}
      {suggestions.map((s) => (
        <FindingMergeSuggestionCard key={s.id} suggestion={s} onResolve={resolve} />
      ))}
      {runs.some((r) => r.state === "queued" || r.state === "running") ? (
        <div className="flex flex-col gap-2">
          {runs
            .filter((r) => r.state === "queued" || r.state === "running")
            .map((r) => (
              <FindingReviewRunRow key={r.conversation_id} run={r} />
            ))}
        </div>
      ) : null}
      {latestFailures.map((r) => (
        <Alert key={r.conversation_id} variant="destructive">
          <AlertDescription>
            <FindingReviewRunRow run={r} />
          </AlertDescription>
        </Alert>
      ))}
      {runs
        .filter((r) => r.state === "done" || r.state === "failed")
        .slice(0, 5)
        .map((r) => (
          <FindingReviewRunRow key={r.conversation_id} run={r} />
        ))}
      {runs.filter((r) => r.state === "done" || r.state === "failed").length > 5 ? (
        <Collapsible open={historyOpen} onOpenChange={setHistoryOpen}>
          <CollapsibleTrigger asChild>
            <Button variant="ghost" size="sm">
              {historyOpen ? uiText("收起整理历史") : uiText("查看整理历史")}（
              {runs.filter((r) => r.state === "done" || r.state === "failed").length}）
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className="flex flex-col gap-2 py-2">
            {runs
              .filter((r) => r.state === "done" || r.state === "failed")
              .map((r) => (
                <FindingReviewRunRow key={r.conversation_id} run={r} />
              ))}
          </CollapsibleContent>
        </Collapsible>
      ) : null}
    </div>
  );
}

export function FindingCaseList({
  query,
  selectedIds,
  onSelect,
  contextTask,
  readOnly = false,
  onTotal,
  refreshToken = 0,
  presentation = "records",
  renderRecords,
}: {
  query: Omit<FindingQuery, "page" | "pageSize">;
  selectedIds?: Set<string>;
  onSelect?: (id: string, checked: boolean) => void;
  contextTask?: string;
  readOnly?: boolean;
  onTotal?: (total: number) => void;
  refreshToken?: number;
  presentation?: "records" | "task" | "asset";
  renderRecords?: (items: Finding[], matchedIds?: number[]) => React.ReactNode;
}) {
  const { t: uiText } = useI18n();
  const [page, setPage] = React.useState(1);
  const [snapshot, setSnapshot] = React.useState<{ key: string; value: FindingCasePage } | null>(null);
  const [failure, setFailure] = React.useState<{ key: string; message: string } | null>(null);
  const [open, setOpen] = React.useState<Set<string>>(() => new Set());
  const [refresh, setRefresh] = React.useState(0);
  const key = JSON.stringify(query);
  const originalRows = Boolean(renderRecords);
  const requestKey = JSON.stringify([key, page, originalRows]);
  const data = snapshot?.key === requestKey ? snapshot.value : null;
  const error = failure?.key === requestKey ? failure.message : "";
  // biome-ignore lint/correctness/useExhaustiveDependencies: reset pagination when the filter fingerprint changes.
  React.useEffect(() => {
    setPage(1);
  }, [key]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: refresh is an explicit reload requested after grouping/retry.
  React.useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    async function load() {
      try {
        const v = await api.findingCases({ ...JSON.parse(key), page, pageSize: 20 }, originalRows);
        if (active) {
          setSnapshot({ key: requestKey, value: v });
          setFailure(null);
        }
      } catch (e) {
        if (active) setFailure({ key: requestKey, message: (e as Error).message });
      } finally {
        if (active) timer = setTimeout(load, 5000);
      }
    }
    void load();
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [key, page, refresh, requestKey, refreshToken, originalRows]);
  React.useEffect(() => {
    onTotal?.(data?.matched_reports ?? 0);
  }, [data, onTotal]);
  React.useEffect(() => {
    if (data && page > Math.max(1, Math.ceil(data.total / 20))) setPage(Math.max(1, Math.ceil(data.total / 20)));
  }, [data, page]);
  const toggle = (id: string) =>
    setOpen((v) => {
      const next = new Set(v);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  return (
    <div className="flex min-w-0 flex-col gap-3">
      {!readOnly ? (
        <FindingCaseReviewPanel
          taskId={query.task === "all" ? undefined : query.task}
          onChange={() => setRefresh((v) => v + 1)}
        />
      ) : null}
      {error ? (
        <Alert>
          <AlertDescription>
            {uiText("加载失败：")}
            {error}
            <Button variant="link" onClick={() => setRefresh((v) => v + 1)}>
              {uiText("重试")}
            </Button>
          </AlertDescription>
        </Alert>
      ) : null}
      {!data && !error ? (
        <div className="flex justify-center p-6">
          <Spinner />
        </div>
      ) : null}
      {data ? (
        <p className="text-muted-foreground text-xs">
          {uiText("当前筛选：{cases} 个独立漏洞 · {reports} 条上报", {
            cases: data.total,
            reports: data.matched_reports,
          })}
        </p>
      ) : null}
      {data?.items.map((row) => {
        if (row.case) {
          const group = row.case;
          return (
            <Card key={`case:${group.id}`} className="gap-0 overflow-hidden py-0">
              <CardHeader className={cn("px-4 py-3", open.has(group.id) && "bg-muted/60")}>
                <div className="flex min-w-0 flex-wrap items-center gap-3">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`${open.has(group.id) ? uiText("收起") : uiText("展开")}${group.title}`}
                    aria-expanded={open.has(group.id)}
                    aria-controls={`finding-case-members-${group.id}`}
                    onClick={() => toggle(group.id)}
                  >
                    <ChevronRightIcon className={cn(open.has(group.id) && "rotate-90")} />
                  </Button>
                  {open.has(group.id) ? (
                    <FolderOpenIcon className="size-5 shrink-0 text-primary" />
                  ) : (
                    <FolderIcon className="size-5 shrink-0 text-muted-foreground" />
                  )}
                  <div className="flex min-w-0 flex-1 basis-2/3 flex-col gap-1 sm:basis-auto">
                    <CardTitle className="line-clamp-2 break-words text-sm sm:truncate">
                      <Link
                        className="hover:underline"
                        title={group.title}
                        href={`/function/findings/case?id=${group.id}${contextTask ? `&context_task=${contextTask}` : ""}`}
                      >
                        {group.title}
                      </Link>
                    </CardTitle>
                    <CardDescription>
                      {group.count} {uiText("条上报 ·")}{" "}
                      {group.report_version !== group.version ? uiText("统一报告待更新") : uiText("统一报告已生成")}
                    </CardDescription>
                    <div className="flex min-w-0 flex-wrap gap-x-3 gap-y-1 text-muted-foreground text-xs">
                      {presentation !== "task" && group.task_id ? (
                        <Link
                          className="max-w-full break-words hover:underline"
                          href={`/function/tasks/detail?id=${group.task_id}`}
                          title={row.task_description}
                        >
                          {uiText("所属任务 #")}
                          {group.task_id}
                          {row.task_description ? ` · ${row.task_description}` : ""}
                        </Link>
                      ) : null}
                      {(row.assets ?? []).length ? (
                        <span className="inline-flex min-w-0 flex-wrap gap-1">
                          {uiText("资产：")}
                          {row.assets?.map((asset) => (
                            <code
                              key={asset.id}
                              className="max-w-48 truncate rounded bg-muted px-1"
                              title={`${asset.type} · ${asset.label}`}
                            >
                              {asset.label}
                            </code>
                          ))}
                          {(row.asset_count ?? 0) > 4 ? <span>+{(row.asset_count ?? 0) - 4}</span> : null}
                        </span>
                      ) : null}
                      {row.last_found_at ? (
                        <time dateTime={row.last_found_at}>
                          {uiText("最近上报")} {new Date(row.last_found_at).toLocaleString()}
                        </time>
                      ) : null}
                    </div>
                  </div>
                  <FindingSeverityCounts counts={group} />
                  <Badge variant="outline">
                    {group.severity
                      ? uiText("统一评级：{v0}", {
                          v0: {
                            critical: uiText("严重"),
                            high: uiText("高危"),
                            medium: uiText("中危"),
                            low: uiText("低危"),
                          }[group.severity],
                        })
                      : uiText("待评估")}
                  </Badge>
                </div>
              </CardHeader>
              {open.has(group.id) ? (
                <CardContent
                  id={`finding-case-members-${group.id}`}
                  role="region"
                  aria-label={`${group.title}的原始子报告`}
                  className="border-t bg-muted/20 px-4 py-4 sm:px-6"
                >
                  <p className="mb-3 text-muted-foreground text-xs">{uiText("原始子报告 · 各自等级和证据保留")}</p>
                  <FindingCaseMembers
                    nested
                    renderRecords={renderRecords ? (items) => renderRecords(items, row.matched_ids) : undefined}
                    caseId={group.id}
                    version={group.version}
                    refreshToken={refreshToken}
                    matchedIds={row.matched_ids}
                    selectedIds={selectedIds}
                    onSelect={readOnly ? undefined : onSelect}
                    contextTask={contextTask}
                  />
                </CardContent>
              ) : null}
            </Card>
          );
        }
        if (row.finding)
          return (
            <Card key={`finding:${row.finding.finding_id ?? row.finding.id}`} className="gap-0 py-0">
              <CardContent className="px-0">
                {renderRecords ? (
                  renderRecords([row.finding])
                ) : (
                  <FindingCaseMemberRow
                    finding={row.finding}
                    selected={selectedIds?.has(row.finding.finding_id ?? row.finding.id)}
                    onSelect={readOnly ? undefined : onSelect}
                    contextTask={contextTask}
                  />
                )}
              </CardContent>
            </Card>
          );
        return null;
      })}
      {data?.total === 0 ? (
        <p className="p-6 text-center text-muted-foreground text-sm">{uiText("暂无匹配漏洞")}</p>
      ) : null}
      {data ? (
        <TablePagination
          page={page}
          pageSize={20}
          total={data.total}
          onPageChange={setPage}
          onPageSizeChange={() => setPage(1)}
          pageSizeOptions={[20]}
        />
      ) : null}
    </div>
  );
}
