"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowDownIcon, ArrowUpIcon, ArrowUpRightIcon, ChevronRightIcon } from "lucide-react";
import { toast } from "sonner";

import { FindingCaseList } from "@/components/finding-case-list";
import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger } from "@/components/ui/select";
import { api } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { useStoredSortPreference } from "@/lib/sort-preference";
import { statusMeta } from "@/lib/status";
import type { Finding, FindingStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

type FindingSortField = "time";

const FINDING_SORT_FIELDS: readonly FindingSortField[] = ["time"];
const FINDING_SORT_PREFERENCE_KEY = "artex_task_findings_sort";

function findingLabel(finding: Finding): string {
  if (finding.name?.trim()) return finding.name;
  if (finding.vulnclass?.trim()) return finding.vulnclass;
  return "";
}

const FINDING_STATUSES: FindingStatus[] = [
  "pending",
  "in_progress",
  "confirmed",
  "resolved",
  "fixed",
  "false_positive",
  "ignored",
  "duplicate",
  "risk_accepted",
];

function Row({
  f,
  contextTaskId,
  onStatus,
}: {
  f: Finding;
  contextTaskId: string;
  onStatus: (f: Finding, next: FindingStatus) => void;
}) {
  const { t: uiText } = useI18n();
  const [open, setOpen] = React.useState(false);
  return (
    <div className="border-b last:border-b-0">
      <div className="flex w-full items-center gap-3 px-4 py-3 text-sm hover:bg-accent/40">
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          className="flex min-w-0 flex-1 items-center gap-3 text-left"
        >
          <ChevronRightIcon
            className={cn("size-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")}
          />
          <StatusBadge domain="severity" value={f.severity} dot />
          <div className="flex min-w-0 flex-1 flex-col gap-1">
            <div className="flex min-w-0 items-center gap-2">
              <span className="truncate font-medium">{findingLabel(f) || uiText("未分类")}</span>
              {f.inherited && f.source_task_id && (
                <Badge variant="outline" className="shrink-0">
                  {uiText("来源 #")}
                  {f.source_task_id} {uiText("· 只读")}
                </Badge>
              )}
            </div>
            <span className="truncate text-muted-foreground text-xs">{f.summary}</span>
          </div>
        </button>
        <Badge variant="outline">
          {uiText("流量证据")} {f.traffic_count ?? 0} {uiText("条")}
        </Badge>
        {f.assets && f.assets.length > 0 && (
          <div className="hidden shrink-0 flex-wrap justify-end gap-1 sm:flex">
            {f.assets.slice(0, 2).map((a) => (
              <code
                key={a.id}
                className="max-w-[10rem] truncate rounded bg-muted px-1.5 py-0.5 font-mono text-xs"
                title={`${a.type} · ${a.label}`}
              >
                {a.label}
              </code>
            ))}
            {f.assets.length > 2 && <span className="text-muted-foreground text-xs">+{f.assets.length - 2}</span>}
          </div>
        )}
        {f.finding_id && !f.inherited ? (
          <Select value={f.status} onValueChange={(v) => onStatus(f, v as FindingStatus)}>
            <SelectTrigger size="sm" className="h-7 w-28 shrink-0 border-none px-1 shadow-none focus-visible:ring-0">
              <StatusBadge domain="finding" value={f.status} dot />
            </SelectTrigger>
            <SelectContent position="popper" align="end">
              <SelectGroup>
                {FINDING_STATUSES.map((st) => (
                  <SelectItem key={st} value={st}>
                    {uiText(statusMeta("finding", st).label)}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        ) : (
          <StatusBadge domain="finding" value={f.status} dot />
        )}
        <span className="hidden shrink-0 text-muted-foreground text-xs md:block">
          {new Date(f.ts).toLocaleString("zh-CN")}
        </span>
        {f.finding_id && (
          <Link
            href={
              f.inherited
                ? `/function/findings/detail?id=${f.finding_id}&context_task=${contextTaskId}`
                : `/function/findings/detail?id=${f.finding_id}`
            }
            className="inline-flex shrink-0 items-center gap-0.5 text-muted-foreground text-xs hover:text-primary"
            title={uiText("查看漏洞详情")}
          >
            {uiText("详情")}
            <ArrowUpRightIcon className="size-3" />
          </Link>
        )}
      </div>
      {open && (
        <div className="bg-muted/30 px-4 pb-4 pl-11">
          <div className="mb-1 font-medium text-muted-foreground text-xs">{uiText("证据 / PoC")}</div>
          <pre className="overflow-auto whitespace-pre-wrap rounded-md border bg-background p-3 font-mono text-xs">
            {f.evidence}
          </pre>
        </div>
      )}
    </div>
  );
}

function FindingsTabInner({ taskId }: { taskId: string }) {
  const { t: uiText } = useI18n();
  const [findings, setFindings] = React.useState<Finding[]>([]);
  const requestVersion = React.useRef(0);
  const [sortPreference, setSortPreference] = useStoredSortPreference(
    FINDING_SORT_PREFERENCE_KEY,
    FINDING_SORT_FIELDS,
    "time",
    "desc",
  );

  React.useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    async function load() {
      const version = ++requestVersion.current;
      try {
        const rows = await api.findings(taskId);
        if (active && version === requestVersion.current) setFindings(rows);
      } catch {
        // Keep the last successful snapshot during transient refresh failures.
      } finally {
        if (active) timer = setTimeout(load, 3000);
      }
    }
    void load();
    return () => {
      active = false;
      requestVersion.current++;
      clearTimeout(timer);
    };
  }, [taskId]);

  const onStatus = React.useCallback(
    async (f: Finding, next: FindingStatus) => {
      if (f.inherited || !f.finding_id || next === f.status) return;
      requestVersion.current++;
      const prev = f.status;
      setFindings((cur) => cur.map((x) => (x.id === f.id ? { ...x, status: next } : x)));
      try {
        await api.setFindingStatus(f.finding_id, next);
        toast.success(uiText("已标记为「{v0}」", { v0: uiText(statusMeta("finding", next).label) }));
      } catch (e) {
        setFindings((cur) => cur.map((x) => (x.id === f.id ? { ...x, status: prev } : x)));
        toast.error(uiText("更新失败：") + (e as Error).message);
      }
    },
    [uiText],
  );

  const items = findings
    .filter((f) => f.task_id === taskId || f.inherited)
    .sort((a, b) => {
      const compared = Date.parse(a.ts) - Date.parse(b.ts);
      if (compared !== 0) return sortPreference.direction === "asc" ? compared : -compared;
      return b.id.localeCompare(a.id, undefined, { numeric: true });
    });

  return (
    <div className="flex flex-col gap-4">
      <FindingCaseList query={{ task: taskId, sort: "time" }} presentation="task" />
      {[
        ...new Set(
          items
            .filter((f) => f.inherited)
            .map((f) => f.source_task_id)
            .filter((id): id is string => !!id),
        ),
      ].map((source) => (
        <div key={source}>
          <p className="mb-2 text-muted-foreground text-sm">
            {uiText("继承任务 #")}
            {source} {uiText("· 只读")}
          </p>
          <FindingCaseList query={{ task: source, sort: "time" }} contextTask={taskId} presentation="task" readOnly />
        </div>
      ))}
      <details>
        <summary className="cursor-pointer text-muted-foreground text-sm">{uiText("查看原始上报列表")}</summary>
        <Card className="overflow-hidden py-0">
          <CardContent className="px-0">
            <div className="flex items-center border-b px-4 py-2 text-muted-foreground text-xs">
              <span className="min-w-0 flex-1">{uiText("漏洞")}</span>
              <button
                type="button"
                className="inline-flex items-center gap-1 outline-none focus-visible:underline"
                aria-label={uiText("发现时间当前{v0}，点击切换排序方向", {
                  v0: sortPreference.direction === "asc" ? uiText("正序") : uiText("倒序"),
                })}
                onClick={() =>
                  setSortPreference((current) => ({
                    field: "time",
                    direction: current.direction === "asc" ? "desc" : "asc",
                  }))
                }
              >
                <span>{uiText("发现时间")}</span>
                {sortPreference.direction === "asc" ? (
                  <ArrowUpIcon className="size-3.5" />
                ) : (
                  <ArrowDownIcon className="size-3.5" />
                )}
              </button>
            </div>
            {items.map((f) => (
              <Row key={f.id} f={f} contextTaskId={taskId} onStatus={onStatus} />
            ))}
            {items.length === 0 && (
              <p className="px-4 py-8 text-center text-muted-foreground text-sm">
                {uiText("本任务及直接关联任务暂无确认发现。")}
              </p>
            )}
          </CardContent>
        </Card>
      </details>
    </div>
  );
}

export function FindingsTab({ taskId }: { taskId: string }) {
  return <FindingsTabInner key={taskId} taskId={taskId} />;
}
