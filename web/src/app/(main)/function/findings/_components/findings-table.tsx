"use client";

import * as React from "react";

import Link from "next/link";

import {
  ArrowUpRightIcon,
  ChevronRightIcon,
  FileTextIcon,
  FlaskConicalIcon,
  FolderIcon,
  FolderOpenIcon,
  RotateCcwIcon,
  ShieldAlertIcon,
  Trash2Icon,
} from "lucide-react";

import { CopyButton } from "@/components/copy-button";
import { FindingCaseMembers, FindingSeverityCounts } from "@/components/finding-case-list";
import { Markdown } from "@/components/markdown";
import { StatusBadge } from "@/components/status-badge";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useI18n } from "@/lib/i18n";
import { statusMeta } from "@/lib/status";
import type { ActiveFindingRetest, Finding, FindingCaseListRow, FindingStatus, Severity } from "@/lib/types";
import { cn } from "@/lib/utils";

export const SEVERITIES: Severity[] = ["critical", "high", "medium", "low"];

export const FINDING_STATUSES: FindingStatus[] = [
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

export const UNASSIGNED_TASK = "__unassigned__";

// 行内编辑缓冲:当前展开行的名称/类别/严重等级。
export interface FindingEdit {
  name: string;
  vulnclass: string;
  severity: Severity;
}

export type FindingReport = { status: "loading" | "done" | "error"; text: string };

// Exploration node ids are only unique inside a task. Prefer the persisted
// finding id and otherwise namespace the node id by task so editing one group
// cannot update a similarly-named node in another expanded group.
export function findingRowKey(finding: Finding): string {
  if (finding.finding_id) return `finding:${finding.finding_id}`;
  return `node:${finding.task_id ?? UNASSIGNED_TASK}:${finding.id}`;
}

export function isSameFinding(left: Finding, right: Finding): boolean {
  return findingRowKey(left) === findingRowKey(right);
}

export function fmtTime(ts: string) {
  return new Date(ts).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

const COLUMN_COUNT = 9;

interface FindingsTableProps {
  items: Finding[];
  caseRows?: FindingCaseListRow[];
  hideHeader?: boolean;
  matchedIds?: number[];
  readOnly?: boolean;
  refreshToken?: number;
  selectedIds: Set<string>;
  onToggleSelected: (id: string, checked: boolean) => void;
  onToggleSelectedPage: (ids: string[], checked: boolean) => void;
  /** 当前展开行的 findingRowKey;null = 全部收起。 */
  expandedKey: string | null;
  onToggleRow: (finding: Finding) => void;
  reports: Record<string, FindingReport>;
  edit: FindingEdit | null;
  onEditChange: React.Dispatch<React.SetStateAction<FindingEdit | null>>;
  saving: boolean;
  onSave: (finding: Finding) => void;
  onStatusChange: (finding: Finding, next: FindingStatus) => void;
  onRetest: (finding: Finding) => void;
  activeRetests: Record<string, ActiveFindingRetest>;
  onDeepen: (finding: Finding) => void;
  onDelete: (finding: Finding) => void;
  /** 全选框的无障碍标签,平铺视图与分组视图措辞不同。 */
  selectAllLabel?: string;
}

// FindingsTable 是发现列表的表格主体,平铺视图与按任务分组视图共用同一份行渲染
// (勾选 / 行内展开 / 行内改名与改状态 / 复测 / 深入 / 删除),差异只在外层容器与分页。
export function FindingsTable({
  items,
  caseRows,
  hideHeader = false,
  matchedIds,
  readOnly = false,
  refreshToken = 0,
  selectedIds,
  onToggleSelected,
  onToggleSelectedPage,
  expandedKey,
  onToggleRow,
  reports,
  edit,
  onEditChange,
  saving,
  onSave,
  onStatusChange,
  onRetest,
  activeRetests,
  onDeepen,
  onDelete,
  selectAllLabel,
}: FindingsTableProps) {
  const { t: uiText } = useI18n();
  const tableId = React.useId();
  const rows: FindingCaseListRow[] = caseRows ?? items.map((finding) => ({ finding, matched_ids: [] }));
  const selectableIds = readOnly
    ? []
    : rows.flatMap((row) =>
        row.case
          ? row.matched_ids.map(String)
          : row.finding &&
              !row.finding.inherited &&
              (!matchedIds || matchedIds.includes(Number(row.finding.finding_id)))
            ? [row.finding.finding_id].filter((id): id is string => Boolean(id))
            : [],
      );
  const shared = {
    selectedIds,
    onToggleSelected,
    onToggleSelectedPage,
    expandedKey,
    onToggleRow,
    reports,
    edit,
    onEditChange,
    saving,
    onSave,
    onStatusChange,
    onRetest,
    activeRetests,
    onDeepen,
    onDelete,
    readOnly,
    refreshToken,
  };
  const selectedCount = selectableIds.filter((id) => selectedIds.has(id)).length;
  let headerChecked: boolean | "indeterminate" = false;
  if (selectableIds.length > 0 && selectedCount === selectableIds.length) {
    headerChecked = true;
  } else if (selectedCount > 0) {
    headerChecked = "indeterminate";
  }

  if (rows.length === 0) {
    return (
      <div className="flex min-h-40 flex-col items-center justify-center gap-2 px-6 py-8 text-center" role="status">
        <FileTextIcon className="size-6 text-muted-foreground" aria-hidden="true" />
        <p className="font-medium text-sm">{uiText("当前条件下暂无发现")}</p>
        <p className="text-muted-foreground text-xs">{uiText("可调整关键词、严重度或任务筛选。")}</p>
      </div>
    );
  }

  return (
    /* 固定列宽保证展开内容不撑开表格；窄屏只在表格内部横向滚动。 */
    <Table
      className="min-w-[68rem] table-fixed"
      containerProps={{
        tabIndex: 0,
        role: "region",
        "aria-label": uiText("漏洞发现列表，可横向滚动"),
        className:
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/50",
      }}
    >
      {!hideHeader && (
        <TableHeader>
          <TableRow>
            <TableHead className="w-8">
              <Checkbox
                checked={headerChecked}
                onCheckedChange={(checked) => onToggleSelectedPage(selectableIds, checked === true)}
                aria-label={selectAllLabel ?? uiText("选择当前页全部")}
              />
            </TableHead>
            <TableHead className="w-12" />
            <TableHead className="w-20">{uiText("严重度")}</TableHead>
            <TableHead className="w-64">{uiText("漏洞名称")}</TableHead>
            <TableHead className="w-40">{uiText("资产")}</TableHead>
            <TableHead className="w-28">{uiText("状态")}</TableHead>
            <TableHead className="w-28">{uiText("所属任务")}</TableHead>
            <TableHead className="w-24">{uiText("时间")}</TableHead>
            <TableHead className="w-48">{uiText("操作")}</TableHead>
          </TableRow>
        </TableHeader>
      )}
      <TableBody>
        {rows.map((row) => {
          if (row.case) return <FindingFolderTableRows key={`case:${row.case.id}`} row={row} shared={shared} />;
          const f = row.finding;
          if (!f) return null;
          const matched = !matchedIds || matchedIds.includes(Number(f.finding_id));
          const canMutate = Boolean(f.finding_id) && !readOnly && !f.inherited;
          const rowKey = findingRowKey(f);
          const findingTitle = f.name || f.vulnclass || uiText("未分类");
          const detailsId = `${tableId}-details-${encodeURIComponent(rowKey)}`;
          const open = expandedKey === rowKey;
          const retest = f.finding_id ? activeRetests[f.finding_id] : undefined;
          return (
            <React.Fragment key={rowKey}>
              <TableRow className={cn("cursor-pointer", !matched && "bg-muted/30")} onClick={() => onToggleRow(f)}>
                <TableCell onClick={(e) => e.stopPropagation()}>
                  {canMutate && matched && (
                    <Checkbox
                      checked={selectedIds.has(f.finding_id as string)}
                      onCheckedChange={(c) => onToggleSelected(f.finding_id as string, c === true)}
                      aria-label={uiText("选择漏洞：{v0}", { v0: findingTitle })}
                    />
                  )}
                </TableCell>
                <TableCell onClick={(e) => e.stopPropagation()}>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={uiText("{v0}漏洞详情：{v1}", {
                      v0: open ? uiText("收起") : uiText("展开"),
                      v1: findingTitle,
                    })}
                    aria-expanded={open}
                    aria-controls={detailsId}
                    onClick={() => onToggleRow(f)}
                  >
                    <ChevronRightIcon
                      aria-hidden="true"
                      className={cn(
                        "size-4 text-muted-foreground transition-transform motion-reduce:transition-none",
                        open && "rotate-90",
                      )}
                    />
                  </Button>
                </TableCell>
                <TableCell>
                  <StatusBadge domain="severity" value={f.severity} dot />
                </TableCell>
                <TableCell className="whitespace-normal">
                  <div className="flex min-w-0 flex-col gap-0.5">
                    {canMutate ? (
                      <Link
                        href={`/function/findings/detail?id=${f.finding_id}`}
                        onClick={(e) => e.stopPropagation()}
                        className="line-clamp-2 break-words font-medium hover:text-primary hover:underline"
                        title={findingTitle}
                      >
                        {findingTitle}
                      </Link>
                    ) : (
                      <span className="line-clamp-2 break-words font-medium" title={findingTitle}>
                        {findingTitle}
                      </span>
                    )}
                    <span className="truncate text-muted-foreground text-xs">{f.summary}</span>
                    {!matched ? <Badge variant="outline">{uiText("未命中当前筛选")}</Badge> : null}
                    {f.inherited || readOnly ? <Badge variant="outline">{uiText("继承 · 只读")}</Badge> : null}
                    <Badge variant="outline">
                      {uiText("流量证据")} {f.traffic_count ?? 0} {uiText("条")}
                    </Badge>
                  </div>
                </TableCell>
                <TableCell>
                  {f.assets && f.assets.length > 0 ? (
                    <div className="flex flex-wrap gap-1">
                      {f.assets.slice(0, 3).map((a) => (
                        <code
                          key={a.id}
                          className="max-w-full truncate rounded bg-muted px-1.5 py-0.5 font-mono text-xs"
                          title={`${a.type} · ${a.label}`}
                        >
                          {a.label}
                        </code>
                      ))}
                      {f.assets.length > 3 && (
                        <span className="text-muted-foreground text-xs">+{f.assets.length - 3}</span>
                      )}
                    </div>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
                <TableCell onClick={(e) => e.stopPropagation()}>
                  {canMutate ? (
                    <Select value={f.status} onValueChange={(v) => onStatusChange(f, v as FindingStatus)}>
                      <SelectTrigger
                        size="sm"
                        className="h-7 w-full border-none px-1 shadow-none"
                        aria-label={uiText("更新漏洞状态：{v0}", { v0: findingTitle })}
                      >
                        <StatusBadge domain="finding" value={f.status} dot />
                      </SelectTrigger>
                      <SelectContent position="popper" align="end">
                        {FINDING_STATUSES.map((st) => (
                          <SelectItem key={st} value={st}>
                            {uiText(statusMeta("finding", st).label)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  ) : (
                    <StatusBadge domain="finding" value={f.status} dot />
                  )}
                </TableCell>
                <TableCell>
                  {f.task_id ? (
                    <Link
                      href={`/function/tasks/detail?id=${f.task_id}`}
                      onClick={(e) => e.stopPropagation()}
                      className="inline-flex max-w-full items-center gap-1 text-primary hover:underline"
                      title={f.task_description}
                    >
                      <span className="truncate">{f.task_description}</span>
                      <ArrowUpRightIcon aria-hidden="true" className="size-3 shrink-0" />
                    </Link>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
                <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(f.ts)}</TableCell>
                <TableCell onClick={(e) => e.stopPropagation()}>
                  <div className="flex items-center gap-1">
                    {retest ? (
                      <Button asChild size="sm" variant="ghost">
                        <Link href={`/chat?c=${retest.conversation_id}`} title={uiText("查看正在进行的复测会话")}>
                          <Spinner aria-hidden="true" data-icon="inline-start" />
                          {uiText("复测中")}
                        </Link>
                      </Button>
                    ) : null}
                    {!retest && canMutate ? (
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => onRetest(f)}
                        title={uiText("在独立会话中复测该漏洞")}
                      >
                        <RotateCcwIcon aria-hidden="true" data-icon="inline-start" />
                        {uiText("复测")}
                      </Button>
                    ) : null}
                    {canMutate && f.task_id && (
                      <Button size="sm" variant="ghost" onClick={() => onDeepen(f)}>
                        <FlaskConicalIcon aria-hidden="true" data-icon="inline-start" />
                        {uiText("深入")}
                      </Button>
                    )}
                    {canMutate && (
                      <AlertDialog>
                        <AlertDialogTrigger asChild>
                          <Button
                            size="icon"
                            variant="ghost"
                            className="size-7 text-muted-foreground hover:text-destructive"
                            aria-label={uiText("删除漏洞：{v0}", { v0: findingTitle })}
                          >
                            <Trash2Icon aria-hidden="true" className="size-4" />
                          </Button>
                        </AlertDialogTrigger>
                        <AlertDialogContent>
                          <AlertDialogHeader>
                            <AlertDialogTitle>{uiText("确认删除该漏洞？")}</AlertDialogTitle>
                            <AlertDialogDescription className="break-words">
                              「
                              <span className="break-all">
                                {f.name || f.vulnclass || f.summary || `#${f.finding_id}`}
                              </span>
                              {uiText("」将被永久删除， 同时从发现列表、任务发现 Tab 与探索图中移除，此操作不可撤销。")}
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter>
                            <AlertDialogCancel>{uiText("取消")}</AlertDialogCancel>
                            <AlertDialogAction onClick={() => onDelete(f)}>{uiText("删除")}</AlertDialogAction>
                          </AlertDialogFooter>
                        </AlertDialogContent>
                      </AlertDialog>
                    )}
                  </div>
                </TableCell>
              </TableRow>
              <TableRow id={detailsId} hidden={!open} className="hover:bg-transparent">
                {/* whitespace-normal 覆盖 TableCell 默认的 nowrap,否则展开区文字
                      被强制单行、直接溢出单元格。 */}
                <TableCell colSpan={COLUMN_COUNT} className="whitespace-normal bg-muted/30">
                  {open && (
                    <div className="flex flex-col gap-2 px-2 py-1">
                      {/* 行内编辑:名称/类别/严重等级,可改并保存(仅独立 finding 行)。 */}
                      {canMutate && edit && (
                        <div className="flex flex-wrap items-end gap-3 rounded-md border bg-background px-3 py-2.5">
                          <div className="flex min-w-[12rem] flex-1 flex-col gap-1">
                            <Label className="text-muted-foreground text-xs">{uiText("漏洞名称")}</Label>
                            <Input
                              value={edit.name}
                              onChange={(e) => onEditChange((s) => (s ? { ...s, name: e.target.value } : s))}
                              placeholder={uiText("可读标题，留空回退类别")}
                            />
                          </div>
                          <div className="flex min-w-[10rem] flex-col gap-1">
                            <Label className="text-muted-foreground text-xs">{uiText("类别")}</Label>
                            <Input
                              value={edit.vulnclass}
                              onChange={(e) => onEditChange((s) => (s ? { ...s, vulnclass: e.target.value } : s))}
                              placeholder={uiText("如 SQL Injection")}
                            />
                          </div>
                          <div className="flex flex-col gap-1">
                            <Label className="text-muted-foreground text-xs">{uiText("严重等级")}</Label>
                            <Select
                              value={edit.severity}
                              onValueChange={(v) => onEditChange((s) => (s ? { ...s, severity: v as Severity } : s))}
                            >
                              <SelectTrigger size="sm" className="w-28">
                                <SelectValue />
                              </SelectTrigger>
                              <SelectContent>
                                {SEVERITIES.map((sv) => (
                                  <SelectItem key={sv} value={sv}>
                                    {uiText(statusMeta("severity", sv).label)}
                                  </SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                          </div>
                          <Button size="sm" disabled={saving} onClick={() => onSave(f)}>
                            {saving ? uiText("保存中…") : uiText("保存")}
                          </Button>
                        </div>
                      )}
                      <div className="flex flex-wrap items-center gap-2 text-muted-foreground text-xs">
                        <ShieldAlertIcon aria-hidden="true" className="size-3.5" />
                        {uiText("证据")}
                        {f.vulnclass && (
                          <span>
                            {uiText("· 类型：")}
                            <code className="rounded bg-muted px-1.5 py-0.5 font-mono">{f.vulnclass}</code>
                          </span>
                        )}
                        {f.param_id && <code className="rounded bg-muted px-1.5 py-0.5 font-mono">{f.param_id}</code>}
                        {f.assets && f.assets.length > 0 && (
                          <span className="flex flex-wrap items-center gap-1">
                            {uiText("· 资产：")}
                            {f.assets.map((a) => (
                              <code key={a.id} className="rounded bg-muted px-1.5 py-0.5 font-mono" title={a.type}>
                                {a.label}
                              </code>
                            ))}
                          </span>
                        )}
                      </div>
                      <pre className="overflow-x-auto whitespace-pre-wrap rounded-md bg-muted px-3 py-2 font-mono text-xs">
                        {f.evidence}
                      </pre>

                      {/* 详细报告(Markdown):展开时按 finding_id 懒加载,免进详情页即可查看。 */}
                      {canMutate && (
                        <div className="flex flex-col gap-1.5">
                          <div className="flex items-center justify-between gap-2 text-muted-foreground text-xs">
                            <span className="flex items-center gap-2">
                              <FileTextIcon aria-hidden="true" className="size-3.5" />
                              {uiText("详细报告")}
                            </span>
                            {reports[rowKey]?.status === "done" && reports[rowKey]?.text.trim() && (
                              <CopyButton
                                text={reports[rowKey]?.text}
                                successMessage={uiText("已复制详细报告")}
                                variant="ghost"
                                className="h-6 px-2 text-xs"
                              />
                            )}
                          </div>
                          {(() => {
                            const rep = reports[rowKey];
                            if (!rep || rep.status === "loading")
                              return <p className="text-muted-foreground text-xs">{uiText("加载中…")}</p>;
                            if (rep.status === "error")
                              return <p className="text-muted-foreground text-xs">{uiText("报告加载失败。")}</p>;
                            if (!rep.text.trim())
                              return <p className="text-muted-foreground text-xs">{uiText("暂无详细报告。")}</p>;
                            return (
                              // break-words 会继承到段落/列表,pre 另加
                              // whitespace-pre-wrap 让代码块也换行——否则长代码行/长 URL
                              // 会撑宽 colSpan 单元格,把整张表挤出横向滚动条。
                              <div className="min-w-0 break-words rounded-md border bg-background px-3 py-2 [&_pre]:whitespace-pre-wrap">
                                <Markdown text={rep.text} />
                              </div>
                            );
                          })()}
                        </div>
                      )}
                    </div>
                  )}
                </TableCell>
              </TableRow>
            </React.Fragment>
          );
        })}
        {items.length === 0 && (
          <TableRow>
            <TableCell colSpan={COLUMN_COUNT} className="py-12 text-center text-muted-foreground text-sm">
              {uiText("没有匹配的发现。")}
            </TableCell>
          </TableRow>
        )}
      </TableBody>
    </Table>
  );
}

// Folder summaries occupy the same columns as the current view's ordinary rows.
// Expanded members reuse those rows without a second heading or column header.
function FindingFolderTableRows({
  row,
  shared,
}: {
  row: FindingCaseListRow;
  shared: Omit<FindingsTableProps, "items" | "caseRows">;
}) {
  const { t: uiText } = useI18n();
  const [open, setOpen] = React.useState(false);
  const group = row.case;
  if (!group) return null;
  // Match ordinary rows, which display the task description.
  const taskLabel = row.task_description;
  const assets = row.assets ?? [];
  const assetCount = row.asset_count ?? assets.length;
  return (
    <>
      <TableRow className={cn(open && "bg-muted/60")}>
        <TableCell />
        <TableCell>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => setOpen((v) => !v)}
            aria-expanded={open}
            aria-controls={`finding-table-members-${group.id}`}
            aria-label={`${open ? uiText("收起") : uiText("展开")}${group.title}`}
          >
            <ChevronRightIcon className={cn("size-4 transition-transform", open && "rotate-90")} />
          </Button>
        </TableCell>
        <TableCell>
          <FindingSeverityCounts counts={group} />
        </TableCell>
        <TableCell className="whitespace-normal">
          <div className="flex min-w-0 items-start gap-2">
            {open ? (
              <FolderOpenIcon className="mt-0.5 size-4 shrink-0 text-primary" />
            ) : (
              <FolderIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
            )}
            <div className="flex min-w-0 flex-col gap-0.5">
              <Link
                className="line-clamp-2 break-words font-medium hover:text-primary hover:underline"
                title={group.title}
                href={`/function/findings/case?id=${group.id}`}
              >
                {group.title}
              </Link>
              <span className="truncate text-muted-foreground text-xs">
                {group.count} {uiText("条上报 ·")}{" "}
                {group.report_version !== group.version ? uiText("统一报告待更新") : uiText("统一报告已生成")}
              </span>
            </div>
          </div>
        </TableCell>
        <TableCell>
          {assets.length ? (
            <div className="flex flex-col gap-0.5">
              {assets.slice(0, 3).map((a) => (
                <code key={a.id} className="max-w-full truncate text-xs" title={`${a.type} · ${a.label}`}>
                  {a.label}
                </code>
              ))}
              {assetCount > 3 ? <span className="text-muted-foreground text-xs">+{assetCount - 3}</span> : null}
            </div>
          ) : (
            <span className="text-muted-foreground">—</span>
          )}
        </TableCell>
        <TableCell>
          <span className="text-muted-foreground" title={uiText("请查看成员各自的处置状态")}>
            —
          </span>
        </TableCell>
        <TableCell>
          {group.task_id ? (
            <Link
              className="inline-flex max-w-full items-center gap-1 text-primary hover:underline"
              title={taskLabel}
              href={`/function/tasks/detail?id=${group.task_id}`}
            >
              <span className="truncate">{taskLabel}</span>
              <ArrowUpRightIcon className="size-3 shrink-0" />
            </Link>
          ) : (
            <span className="text-muted-foreground">—</span>
          )}
        </TableCell>
        <TableCell className="text-muted-foreground text-xs tabular-nums">
          {row.last_found_at ? fmtTime(row.last_found_at) : "—"}
        </TableCell>
        <TableCell>
          <Button asChild size="sm" variant="ghost">
            <Link href={`/function/findings/case?id=${group.id}`}>{uiText("统一报告")}</Link>
          </Button>
        </TableCell>
      </TableRow>
      {open ? (
        <TableRow>
          <TableCell colSpan={COLUMN_COUNT} className="whitespace-normal bg-muted/20 px-4 py-4">
            <section
              id={`finding-table-members-${group.id}`}
              aria-label={uiText("{title}的原始上报", { title: group.title })}
            >
              <FindingCaseMembers
                caseId={group.id}
                version={group.version}
                refreshToken={shared.refreshToken}
                nested
                renderRecords={(members) => (
                  <FindingsTable {...shared} items={members} matchedIds={row.matched_ids} hideHeader />
                )}
              />
            </section>
          </TableCell>
        </TableRow>
      ) : null}
    </>
  );
}
