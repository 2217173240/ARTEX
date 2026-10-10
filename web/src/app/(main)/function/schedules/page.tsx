"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import Link from "next/link";

import {
  CalendarClockIcon,
  ClockIcon,
  HistoryIcon,
  Loader2Icon,
  PauseIcon,
  PencilIcon,
  PlayIcon,
  PlusIcon,
  RefreshCwIcon,
  Trash2Icon,
} from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { MOCK } from "@/lib/mock/enabled";
import { localClock, type ScheduleInput, scheduleWindow, WEEKDAY_LABELS } from "@/lib/schedule-window";
import { type ScheduleDetail, type ScheduleRunResult, schedulesApi, type TaskSchedule } from "@/lib/schedules";
import type { Task } from "@/lib/types";

import { ScheduleCalendar } from "./schedule-calendar";
import { ScheduleEditor } from "./schedule-editor";

const errorMessage = (error: unknown) => (error instanceof Error ? error.message : String(error));

export default function SchedulesPage() {
  const { t, locale } = useI18n();
  const [rows, setRows] = useState<TaskSchedule[]>([]);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [tasksError, setTasksError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [editor, setEditor] = useState<{ row: TaskSchedule | null; day?: string } | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [deleteRow, setDeleteRow] = useState<TaskSchedule | null>(null);
  const [selected, setSelected] = useState<TaskSchedule | null>(null);
  const [detail, setDetail] = useState<ScheduleDetail | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [runResults, setRunResults] = useState<{ results: ScheduleRunResult[]; manual_until?: string | null } | null>(
    null,
  );
  const [snapshotTime, setSnapshotTime] = useState(() => new Date());
  const mounted = useRef(false);
  const request = useRef(0);
  const detailRequest = useRef(0);
  const refresh = useCallback(async () => {
    const revision = ++request.current;
    if (mounted.current) setLoading(true);
    // /tasks returns the full live collection; task search never truncates membership.
    const [scheduleResponse, taskResponse] = await Promise.allSettled([schedulesApi.list(), api.tasks()]);
    if (!mounted.current || revision !== request.current) return;
    if (scheduleResponse.status === "fulfilled") {
      setRows(scheduleResponse.value);
      setLoadError(null);
      setSnapshotTime(new Date());
    } else setLoadError(errorMessage(scheduleResponse.reason));
    if (taskResponse.status === "fulfilled") {
      setTasks(taskResponse.value.tasks);
      setTasksError(null);
    } else setTasksError(errorMessage(taskResponse.reason));
    setLoading(false);
  }, []);
  useEffect(() => {
    mounted.current = true;
    void refresh();
    return () => {
      mounted.current = false;
      request.current++;
      detailRequest.current++;
    };
  }, [refresh]);
  const loadDetail = useCallback(async (row: TaskSchedule) => {
    const revision = ++detailRequest.current;
    setSelected(row);
    setDetail(null);
    setDetailError(null);
    setDetailLoading(true);
    try {
      const response = await schedulesApi.detail(row.id);
      if (mounted.current && revision === detailRequest.current) setDetail(response);
    } catch (error) {
      if (mounted.current && revision === detailRequest.current) setDetailError(errorMessage(error));
    } finally {
      if (mounted.current && revision === detailRequest.current) setDetailLoading(false);
    }
  }, []);
  const perform = async (key: string, action: () => Promise<void>) => {
    if (busy) return;
    setBusy(key);
    setActionError(null);
    try {
      await action();
    } catch (error) {
      if (mounted.current) setActionError(errorMessage(error));
    } finally {
      if (mounted.current) setBusy(null);
    }
  };
  const save = async (input: ScheduleInput) => {
    if (!editor || busy) return;
    setBusy("save");
    setSaveError(null);
    try {
      if (editor.row) await schedulesApi.update(editor.row.id, input);
      else await schedulesApi.create(input);
      if (!mounted.current) return;
      setEditor(null);
      await refresh();
    } catch (error) {
      if (mounted.current) setSaveError(errorMessage(error));
    } finally {
      if (mounted.current) setBusy(null);
    }
  };
  const openEditor = (row: TaskSchedule | null, day?: string) => {
    setSaveError(null);
    setEditor({ row, day });
  };
  const taskName = (id: string) => {
    const task = tasks.find((item) => item.id === id);
    return task?.name || task?.description || `#${id}`;
  };
  const windowFor = (row: TaskSchedule) => {
    if (MOCK) return scheduleWindow(row, snapshotTime);
    if (!row.next_start || !row.next_end) return null;
    return {
      start: localClock(new Date(row.next_start), row.timezone).slice(0, 16),
      end: localClock(new Date(row.next_end), row.timezone).slice(0, 16),
      active: row.status === "running",
    };
  };
  const manualActive = (row: TaskSchedule) => !!row.manual_until && new Date(row.manual_until) > snapshotTime;
  const statusText = (row: TaskSchedule) => {
    if (!row.enabled || row.status === "paused") return t("计划已暂停");
    if (manualActive(row)) return t("手动执行窗口");
    const window = windowFor(row);
    if (row.status === "running" || window?.active) return t("窗口已开放");
    return row.status === "scheduled" || window ? t("等待窗口") : t("窗口已结束");
  };
  const dateTime = (value: string) => new Date(value).toLocaleString(locale);
  const upcoming = rows
    .map((row) => ({ row, window: windowFor(row) }))
    .filter((item) => item.window)
    .sort((a, b) => {
      if (!MOCK) return new Date(a.row.next_start ?? 0).getTime() - new Date(b.row.next_start ?? 0).getTime();
      return (a.window?.start ?? "").localeCompare(b.window?.start ?? "");
    });
  const active = rows.filter(
    (row) => row.enabled && (row.status === "running" || windowFor(row)?.active || manualActive(row)),
  ).length;
  const recurrence = (row: TaskSchedule) =>
    row.type === "weekly"
      ? row.weekdays.map((day) => t(WEEKDAY_LABELS[day - 1] ?? "")).join(" · ")
      : `${row.run_date} → ${row.end_date || row.run_date}`;
  const historyStatus = (value: string) =>
    t(
      (
        { running: "运行中", queued: "排队中", paused: "已暂停", failed: "失败", skipped: "已跳过" } as Record<
          string,
          string
        >
      )[value] ?? value,
    );
  const historyAction = (value: string) =>
    t(
      ({ "run-now": "立即运行", resume: "窗口开始", pause: "窗口结束", start: "窗口开始" } as Record<string, string>)[
        value
      ] ?? value,
    );
  const actionButtons = (row: TaskSchedule) => (
    <div className="flex flex-wrap items-center gap-1">
      <Button
        size="sm"
        variant="outline"
        disabled={!!busy || MOCK}
        title={MOCK ? t("预览模式不会启动任务") : t("立即运行会启用计划，并持续至当前或下一个窗口结束。")}
        onClick={() => {
          void perform(`run-${row.id}`, async () => {
            const response = await schedulesApi.runNow(row.id);
            if (!mounted.current) return;
            setRunResults(response);
            await refresh();
          });
        }}
      >
        <PlayIcon className="size-3.5" />
        {t("立即运行")}
      </Button>
      <Button
        size="sm"
        variant="ghost"
        disabled={!!busy}
        onClick={() => {
          void perform(`enabled-${row.id}`, async () => {
            await schedulesApi.setEnabled(row, !row.enabled);
            await refresh();
          });
        }}
      >
        {row.enabled ? <PauseIcon className="size-3.5" /> : <PlayIcon className="size-3.5" />}
        {t(row.enabled ? "暂停计划" : "恢复计划")}
      </Button>
      <Button
        size="icon-sm"
        variant="ghost"
        disabled={!!busy}
        aria-label={t("编辑 {name}", { name: row.name })}
        onClick={() => openEditor(row)}
      >
        <PencilIcon className="size-4" />
      </Button>
      <Button
        size="icon-sm"
        variant="ghost"
        disabled={!!busy}
        aria-label={t("查看 {name} 的历史", { name: row.name })}
        onClick={() => {
          void loadDetail(row);
        }}
      >
        <HistoryIcon className="size-4" />
      </Button>
      <Button
        size="icon-sm"
        variant="ghost"
        disabled={!!busy}
        aria-label={t("删除 {name}", { name: row.name })}
        onClick={() => {
          setActionError(null);
          setDeleteRow(row);
        }}
      >
        <Trash2Icon className="size-4" />
      </Button>
    </div>
  );
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="mb-2 flex items-center gap-2 text-primary text-xs">
            <CalendarClockIcon className="size-4" />
            ARTEX · {t("执行时间管理")}
          </div>
          <h1 className="font-semibold text-2xl tracking-tight">{t("任务日历")}</h1>
          <p className="mt-2 max-w-2xl text-muted-foreground text-sm">
            {t("将已有任务加入计划，在需要的时间开放执行窗口；系统按可用并发运行或排队。")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            disabled={loading || !!busy}
            onClick={() => {
              setActionError(null);
              void refresh();
            }}
          >
            <RefreshCwIcon className={loading ? "size-4 animate-spin" : "size-4"} />
            {t("刷新")}
          </Button>
          <Button disabled={loading || !!busy} onClick={() => openEditor(null)}>
            <PlusIcon className="size-4" />
            {t("新增计划")}
          </Button>
        </div>
      </div>
      {MOCK && (
        <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 px-4 py-3 text-sm">
          {t("预览模式：计划仅保存在当前页面会话，不会启动真实任务。")}
        </div>
      )}
      <div className="grid gap-3 sm:grid-cols-3">
        {[
          { label: "全部计划", value: rows.length, icon: CalendarClockIcon },
          { label: "窗口已开放", value: active, icon: ClockIcon },
          { label: "计划已暂停", value: rows.filter((row) => !row.enabled).length, icon: PauseIcon },
        ].map(({ label, value, icon: Icon }) => (
          <Card key={label}>
            <CardContent className="flex items-center justify-between p-4">
              <div>
                <p className="text-muted-foreground text-xs">{t(label)}</p>
                <p className="mt-1 font-semibold text-2xl tabular-nums">
                  {(loading || !!loadError) && !rows.length ? "—" : value}
                </p>
              </div>
              <Icon className="size-5 text-muted-foreground" />
            </CardContent>
          </Card>
        ))}
      </div>
      <p className="text-muted-foreground text-xs">
        {t("窗口状态表示允许执行；任务实际状态以任务页的运行中、排队或暂停为准。")}
      </p>
      {(loadError ?? actionError) && (
        <div
          role="alert"
          className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-destructive/30 p-3 text-destructive text-sm"
        >
          <span>{loadError ?? actionError}</span>
          <Button
            variant="outline"
            disabled={loading || !!busy}
            onClick={() => {
              setActionError(null);
              void refresh();
            }}
          >
            {t("重试")}
          </Button>
        </div>
      )}
      {loading && !rows.length ? (
        <Card>
          <CardContent className="flex items-center justify-center gap-2 py-20 text-muted-foreground">
            <Loader2Icon className="size-5 animate-spin" />
            {t("加载计划中…")}
          </CardContent>
        </Card>
      ) : (
        <>
          <div className="grid items-start gap-5 xl:grid-cols-[minmax(0,1fr)_280px]">
            <Card>
              <CardContent className="p-3 sm:p-5">
                <ScheduleCalendar
                  schedules={rows}
                  onSelect={(row) => {
                    void loadDetail(row);
                  }}
                  onCreate={(day) => openEditor(null, day)}
                />
              </CardContent>
            </Card>
            <Card>
              <CardContent className="space-y-4 p-5">
                <div>
                  <h2 className="font-semibold text-sm">{t("接下来")}</h2>
                  <p className="mt-1 text-muted-foreground text-xs">{t("各计划的当地时间")}</p>
                </div>
                {upcoming.length ? (
                  upcoming.slice(0, 5).map(({ row, window }) => (
                    <button
                      type="button"
                      key={row.id}
                      onClick={() => {
                        void loadDetail(row);
                      }}
                      className="block w-full space-y-1 rounded-lg border-l-2 border-l-primary bg-muted/25 px-3 py-2 text-left hover:bg-muted/50"
                    >
                      <p className="truncate font-medium text-sm">{row.name}</p>
                      <p className="text-muted-foreground text-xs">{window?.start}</p>
                      <p className="text-muted-foreground text-xs">→ {window?.end}</p>
                      <p className="text-[10px] text-muted-foreground">
                        {row.timezone} · {statusText(row)}
                      </p>
                    </button>
                  ))
                ) : (
                  <p className="py-4 text-muted-foreground text-sm">{t("暂无即将到来的窗口")}</p>
                )}
                <p className="border-t pt-3 text-muted-foreground text-xs">
                  {t("完成和失败的任务不会自动重启，人工暂停也会保留。")}
                </p>
              </CardContent>
            </Card>
          </div>
          <Card>
            <CardContent className="p-0">
              <div className="flex flex-wrap items-center justify-between gap-2 border-b p-4">
                <h2 className="font-semibold text-sm">{t("计划列表")}</h2>
                <span className="text-muted-foreground text-xs">
                  {t("更新于 {time}", { time: snapshotTime.toLocaleTimeString(locale) })}
                </span>
              </div>
              {!rows.length ? (
                <div className="space-y-3 py-12 text-center">
                  <CalendarClockIcon className="mx-auto size-9 text-muted-foreground" />
                  <h3 className="font-medium">{t(loadError ? "计划加载失败" : "还没有执行计划")}</h3>
                  <p className="mx-auto max-w-sm px-4 text-muted-foreground text-sm">
                    {t(loadError ? "请重试以加载计划" : "创建每周巡检或单次执行窗口，把已有任务安排到合适的时间。")}
                  </p>
                  <Button disabled={!!loadError} onClick={() => openEditor(null)}>
                    <PlusIcon className="size-4" />
                    {t("创建第一个计划")}
                  </Button>
                </div>
              ) : (
                <>
                  <div className="hidden overflow-x-auto md:block">
                    <Table>
                      <TableHeader>
                        <TableRow>
                          <TableHead>{t("计划 / 任务")}</TableHead>
                          <TableHead>{t("执行窗口")}</TableHead>
                          <TableHead>{t("窗口状态")}</TableHead>
                          <TableHead>{t("操作")}</TableHead>
                        </TableRow>
                      </TableHeader>
                      <TableBody>
                        {rows.map((row) => (
                          <TableRow key={row.id}>
                            <TableCell>
                              <button
                                type="button"
                                onClick={() => {
                                  void loadDetail(row);
                                }}
                                className="max-w-64 truncate font-medium hover:underline"
                              >
                                {row.name}
                              </button>
                              <p className="mt-1 text-muted-foreground text-xs">
                                {t("{count} 个关联任务", { count: row.task_ids.length })}
                              </p>
                            </TableCell>
                            <TableCell>
                              <p className="max-w-72 whitespace-normal text-xs">{recurrence(row)}</p>
                              <p className="mt-1 font-medium text-sm">
                                {row.start_time}–{row.end_time}
                              </p>
                              <p className="mt-1 text-muted-foreground text-xs">{row.timezone}</p>
                            </TableCell>
                            <TableCell>
                              <Badge variant={windowFor(row)?.active ? "default" : "secondary"}>
                                {statusText(row)}
                              </Badge>
                              {row.manual_until && manualActive(row) && (
                                <p className="mt-1 max-w-48 whitespace-normal text-muted-foreground text-xs">
                                  {t("手动执行至 {time}", { time: dateTime(row.manual_until) })}
                                </p>
                              )}
                            </TableCell>
                            <TableCell>{actionButtons(row)}</TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  </div>
                  <div className="divide-y md:hidden">
                    {rows.map((row) => (
                      <div key={row.id} className="space-y-3 p-4">
                        <div className="flex items-start justify-between gap-2">
                          <button
                            type="button"
                            className="min-w-0 truncate text-left font-medium"
                            onClick={() => {
                              void loadDetail(row);
                            }}
                          >
                            {row.name}
                          </button>
                          <Badge variant="secondary">{statusText(row)}</Badge>
                        </div>
                        <p className="text-muted-foreground text-xs">
                          {recurrence(row)} · {row.start_time}–{row.end_time}
                          <br />
                          {row.timezone} · {t("{count} 个关联任务", { count: row.task_ids.length })}
                        </p>
                        {actionButtons(row)}
                      </div>
                    ))}
                  </div>
                </>
              )}
            </CardContent>
          </Card>
        </>
      )}
      {editor && (
        <ScheduleEditor
          key={editor.row?.id ?? "new"}
          {...editor}
          tasks={tasks}
          tasksError={tasksError}
          busy={busy === "save"}
          error={saveError}
          onClose={() => setEditor(null)}
          onRetryTasks={() => {
            void refresh();
          }}
          onSave={(input) => {
            void save(input);
          }}
        />
      )}
      <Dialog
        open={!!selected}
        onOpenChange={(open) => {
          if (!open) {
            detailRequest.current++;
            setSelected(null);
          }
        }}
      >
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{selected?.name}</DialogTitle>
            <DialogDescription>{t("关联任务与最近 50 条调度记录")}</DialogDescription>
          </DialogHeader>
          {detailLoading && (
            <p className="flex items-center gap-2 text-muted-foreground text-sm">
              <Loader2Icon className="size-4 animate-spin" />
              {t("加载中…")}
            </p>
          )}
          {detailError && (
            <div role="alert" className="space-y-2 text-destructive text-sm">
              <p>{detailError}</p>
              <Button
                variant="outline"
                onClick={() => {
                  if (selected) void loadDetail(selected);
                }}
              >
                {t("重试")}
              </Button>
            </div>
          )}
          {detail && (
            <div className="space-y-5">
              <div className="rounded-lg bg-muted/30 p-3 text-sm">
                <Badge variant="secondary">{statusText(detail.schedule)}</Badge>
                <p className="mt-2">
                  {recurrence(detail.schedule)} · {detail.schedule.start_time}–{detail.schedule.end_time} ·{" "}
                  {detail.schedule.timezone}
                </p>
                {windowFor(detail.schedule) && (
                  <p className="mt-1 text-muted-foreground text-xs">
                    {t("下一个或当前窗口")} · {windowFor(detail.schedule)?.start} → {windowFor(detail.schedule)?.end}
                  </p>
                )}
                {detail.schedule.manual_until && manualActive(detail.schedule) && (
                  <p className="mt-1 text-muted-foreground text-xs">
                    {t("手动执行至 {time}", { time: dateTime(detail.schedule.manual_until) })}
                  </p>
                )}
              </div>
              <div>
                <h3 className="mb-2 font-medium text-sm">{t("关联任务")}</h3>
                <div className="max-h-48 space-y-2 overflow-y-auto">
                  {detail.schedule.task_ids.map((id) => {
                    const task = tasks.find((row) => row.id === id);
                    return (
                      <div key={id} className="flex items-center justify-between gap-2 rounded-lg border px-3 py-2">
                        <Link
                          href={`/function/tasks/detail?id=${encodeURIComponent(id)}`}
                          className="min-w-0 truncate text-sm hover:underline"
                        >
                          {taskName(id)}
                        </Link>
                        {task ? (
                          <StatusBadge domain="task" value={task.status} />
                        ) : (
                          <Badge variant="outline">{t("任务不可用")}</Badge>
                        )}
                      </div>
                    );
                  })}
                </div>
                {tasksError && (
                  <p role="alert" className="mt-2 text-destructive text-xs">
                    {tasksError}
                  </p>
                )}
              </div>
              <div>
                <h3 className="mb-2 font-medium text-sm">{t("调度历史")}</h3>
                {!detail.history.length ? (
                  <p className="text-muted-foreground text-sm">{t("暂无调度记录")}</p>
                ) : (
                  <div className="max-h-72 space-y-2 overflow-y-auto">
                    {detail.history.map((run) => (
                      <div key={run.id} className="rounded-lg border p-3">
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <p className="text-sm">{taskName(run.task_id)}</p>
                          <Badge variant={run.error ? "destructive" : "secondary"}>{historyStatus(run.status)}</Badge>
                        </div>
                        <p className="mt-1 text-muted-foreground text-xs">
                          {historyAction(run.action)} · {dateTime(run.created_at)}
                        </p>
                        {run.error && <p className="mt-2 text-destructive text-xs">{run.error}</p>}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!deleteRow}
        onOpenChange={(open) => {
          if (!open && !busy) setDeleteRow(null);
        }}
      >
        <DialogContent showCloseButton={!busy}>
          <DialogHeader>
            <DialogTitle>{t("删除计划")}</DialogTitle>
            <DialogDescription>
              {t("删除“{name}”及其调度记录？关联任务会保留，之后不再受此计划调度。", { name: deleteRow?.name ?? "" })}
            </DialogDescription>
          </DialogHeader>
          {actionError && (
            <p role="alert" className="text-destructive text-sm">
              {actionError}
            </p>
          )}
          <DialogFooter>
            <Button variant="outline" disabled={!!busy} onClick={() => setDeleteRow(null)}>
              {t("取消")}
            </Button>
            <Button
              variant="destructive"
              disabled={!!busy}
              onClick={() => {
                if (deleteRow)
                  void perform(`delete-${deleteRow.id}`, async () => {
                    await schedulesApi.remove(deleteRow.id);
                    if (mounted.current) setDeleteRow(null);
                    await refresh();
                  });
              }}
            >
              {t(busy ? "删除中…" : "删除计划")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!runResults}
        onOpenChange={(open) => {
          if (!open) setRunResults(null);
        }}
      >
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>{t("立即运行结果")}</DialogTitle>
            <DialogDescription>{t("任务会按并发容量进入运行或排队；单个任务的失败不会隐藏。")}</DialogDescription>
          </DialogHeader>
          {runResults?.manual_until && (
            <p className="text-muted-foreground text-sm">
              {t("手动执行至 {time}", { time: dateTime(runResults.manual_until) })}
            </p>
          )}
          <div className="max-h-80 space-y-2 overflow-y-auto">
            {runResults?.results.map((result) => (
              <div key={result.id} className="rounded-lg border p-3">
                <div className="flex items-center justify-between gap-2">
                  <Link
                    href={`/function/tasks/detail?id=${encodeURIComponent(result.id)}`}
                    className="min-w-0 truncate text-sm hover:underline"
                  >
                    {taskName(result.id)}
                  </Link>
                  {result.ok && result.status ? (
                    <StatusBadge domain="task" value={result.status} />
                  ) : (
                    <Badge variant="destructive">{t("失败")}</Badge>
                  )}
                </div>
                {result.error && <p className="mt-2 text-destructive text-sm">{result.error}</p>}
              </div>
            ))}
          </div>
          <DialogFooter>
            <Button onClick={() => setRunResults(null)}>{t("关闭")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
