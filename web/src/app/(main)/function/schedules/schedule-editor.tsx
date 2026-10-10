"use client";

import { useMemo, useState } from "react";

import { SearchIcon } from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/lib/i18n";
import {
  isSchedulableTask,
  localClock,
  type ScheduleInput,
  scheduleWindow,
  validateSchedule,
  WEEKDAY_LABELS,
} from "@/lib/schedule-window";
import type { TaskSchedule } from "@/lib/schedules";
import type { Task } from "@/lib/types";

export function ScheduleEditor({
  row,
  day,
  tasks,
  tasksError,
  busy,
  error,
  onRetryTasks,
  onClose,
  onSave,
}: {
  row: TaskSchedule | null;
  day?: string;
  tasks: Task[];
  tasksError: string | null;
  busy: boolean;
  error: string | null;
  onRetryTasks: () => void;
  onClose: () => void;
  onSave: (input: ScheduleInput) => void;
}) {
  const { t } = useI18n();
  const today = day ?? localClock(new Date(), "Asia/Shanghai").slice(0, 10);
  const [form, setForm] = useState<ScheduleInput>(() =>
    row
      ? {
          name: row.name,
          enabled: row.enabled,
          type: row.type,
          timezone: row.timezone,
          run_date: row.run_date || today,
          end_date: row.end_date || row.run_date || today,
          weekdays: row.weekdays ?? [],
          start_time: row.start_time,
          end_time: row.end_time,
          task_ids: [...row.task_ids],
        }
      : {
          name: "",
          enabled: true,
          type: day ? "once" : "weekly",
          timezone: "Asia/Shanghai",
          run_date: today,
          end_date: today,
          weekdays: [1, 2, 3, 4, 5],
          start_time: "09:00",
          end_time: "18:00",
          task_ids: [],
        },
  );
  const [query, setQuery] = useState("");
  const [validation, setValidation] = useState<string | null>(null);
  const set = <K extends keyof ScheduleInput>(key: K, value: ScheduleInput[K]) =>
    setForm((previous) => ({ ...previous, [key]: value }));
  const eligible = useMemo(() => tasks.filter(isSchedulableTask), [tasks]);
  const visible = useMemo(
    () =>
      eligible.filter((task) =>
        `${task.id} ${task.name ?? ""} ${task.description}`.toLowerCase().includes(query.toLowerCase()),
      ),
    [eligible, query],
  );
  const unavailable = form.task_ids.filter((id) => !eligible.some((task) => task.id === id));
  const window = scheduleWindow({ ...form, enabled: true });
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DialogContent className="sm:max-w-3xl" showCloseButton={!busy}>
        <DialogHeader>
          <DialogTitle>{t(row ? "编辑计划" : "新增计划")}</DialogTitle>
          <DialogDescription>{t("选择已有任务，在指定时间窗口内允许执行。并发已满时任务会排队。")}</DialogDescription>
        </DialogHeader>
        <form
          className="space-y-5"
          onSubmit={(event) => {
            event.preventDefault();
            const issue = validateSchedule(form);
            setValidation(issue);
            if (!issue && !tasksError && !unavailable.length)
              onSave({
                ...form,
                name: form.name.trim(),
                timezone: form.timezone.trim(),
                weekdays: form.type === "weekly" ? form.weekdays : [],
                run_date: form.type === "once" ? form.run_date : "",
                end_date: form.type === "once" ? form.end_date : "",
              });
          }}
        >
          <fieldset disabled={busy} className="space-y-5">
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="schedule-name">{t("计划名称")}</Label>
                <Input
                  id="schedule-name"
                  value={form.name}
                  onChange={(event) => set("name", event.target.value)}
                  placeholder={t("例如：工作日巡检")}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="schedule-timezone">{t("时区")}</Label>
                <Input
                  id="schedule-timezone"
                  value={form.timezone}
                  onChange={(event) => set("timezone", event.target.value)}
                  placeholder="Asia/Shanghai"
                  list="schedule-timezones"
                  required
                />
                <datalist id="schedule-timezones">
                  <option value="Asia/Shanghai" />
                  <option value="Asia/Singapore" />
                  <option value="UTC" />
                  <option value="America/New_York" />
                  <option value="Europe/London" />
                </datalist>
              </div>
            </div>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex rounded-lg border p-1">
                <Button
                  type="button"
                  size="sm"
                  variant={form.type === "weekly" ? "secondary" : "ghost"}
                  aria-pressed={form.type === "weekly"}
                  onClick={() => set("type", "weekly")}
                >
                  {t("每周重复")}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant={form.type === "once" ? "secondary" : "ghost"}
                  aria-pressed={form.type === "once"}
                  onClick={() => set("type", "once")}
                >
                  {t("单次窗口")}
                </Button>
              </div>
              <div className="flex items-center gap-2">
                <Switch
                  id="schedule-enabled"
                  checked={form.enabled}
                  onCheckedChange={(value) => set("enabled", value)}
                />
                <Label htmlFor="schedule-enabled">{t("启用计划")}</Label>
              </div>
            </div>
            {form.type === "weekly" ? (
              <div className="flex flex-wrap gap-2">
                {WEEKDAY_LABELS.map((label, index) => (
                  <Button
                    type="button"
                    key={label}
                    size="sm"
                    variant={form.weekdays.includes(index + 1) ? "default" : "outline"}
                    aria-pressed={form.weekdays.includes(index + 1)}
                    onClick={() =>
                      set(
                        "weekdays",
                        form.weekdays.includes(index + 1)
                          ? form.weekdays.filter((day) => day !== index + 1)
                          : [...form.weekdays, index + 1].sort(),
                      )
                    }
                  >
                    {t(label)}
                  </Button>
                ))}
              </div>
            ) : (
              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="schedule-start-date">{t("开始日期")}</Label>
                  <Input
                    id="schedule-start-date"
                    type="date"
                    value={form.run_date}
                    onChange={(event) => set("run_date", event.target.value)}
                    required
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="schedule-end-date">{t("结束日期")}</Label>
                  <Input
                    id="schedule-end-date"
                    type="date"
                    value={form.end_date}
                    onChange={(event) => set("end_date", event.target.value)}
                    required
                  />
                </div>
              </div>
            )}
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="schedule-start-time">{t("开始时间")}</Label>
                <Input
                  id="schedule-start-time"
                  type="time"
                  value={form.start_time}
                  onChange={(event) => set("start_time", event.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="schedule-end-time">{t("结束时间")}</Label>
                <Input
                  id="schedule-end-time"
                  type="time"
                  value={form.end_time}
                  onChange={(event) => set("end_time", event.target.value)}
                  required
                />
              </div>
            </div>
            <div className="rounded-lg border bg-muted/30 px-3 py-2 text-muted-foreground text-xs">
              {form.type === "weekly" && form.end_time < form.start_time && (
                <p>{t("结束时间早于开始时间时，窗口延续至次日。")}</p>
              )}
              {form.type === "once" && <p>{t("日期范围是一个连续窗口，不会每天重新开始。")}</p>}
              {window && (
                <p className="mt-1">
                  {t("窗口预览")} · {window.start} → {window.end} · {form.timezone}
                </p>
              )}
            </div>
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <Label>{t("关联任务")}</Label>
                <Badge variant="secondary">{t("已选 {count} / 100", { count: form.task_ids.length })}</Badge>
              </div>
              <p className="text-muted-foreground text-xs">
                {t("完成或失败的任务不可加入；人工暂停的任务不会被计划自动唤醒。")}
              </p>
              <div className="relative">
                <SearchIcon className="absolute top-2.5 left-3 size-4 text-muted-foreground" />
                <Input
                  className="pl-9"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  placeholder={t("搜索任务名称、描述或 ID")}
                  aria-label={t("搜索关联任务")}
                />
              </div>
              {tasksError ? (
                <div role="alert" className="rounded-lg border border-destructive/30 p-3 text-sm">
                  <p>{tasksError}</p>
                  <Button type="button" variant="outline" size="sm" onClick={onRetryTasks}>
                    {t("重新加载任务")}
                  </Button>
                </div>
              ) : (
                <div className="max-h-52 overflow-y-auto rounded-lg border">
                  {visible.map((task) => (
                    <label
                      key={task.id}
                      htmlFor={`schedule-task-${task.id}`}
                      className="flex cursor-pointer items-center gap-3 border-b px-3 py-2 last:border-b-0 hover:bg-muted/40"
                    >
                      <Checkbox
                        id={`schedule-task-${task.id}`}
                        checked={form.task_ids.includes(task.id)}
                        disabled={!form.task_ids.includes(task.id) && form.task_ids.length >= 100}
                        onCheckedChange={(checked) =>
                          set(
                            "task_ids",
                            checked ? [...form.task_ids, task.id] : form.task_ids.filter((id) => id !== task.id),
                          )
                        }
                      />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-sm">{task.name || task.description || task.id}</span>
                        <span className="text-muted-foreground text-xs">#{task.id}</span>
                      </span>
                      <StatusBadge domain="task" value={task.status} />
                    </label>
                  ))}
                  {!visible.length && (
                    <p className="p-5 text-center text-muted-foreground text-sm">{t("没有匹配的可用任务")}</p>
                  )}
                </div>
              )}
              {unavailable.length > 0 && (
                <div className="space-y-2 rounded-lg border border-amber-500/30 p-3">
                  <p className="text-sm">{t("以下关联任务已不可用，请移除后保存")}</p>
                  <div className="flex flex-wrap gap-2">
                    {unavailable.map((id) => (
                      <Button
                        key={id}
                        type="button"
                        size="sm"
                        variant="outline"
                        onClick={() =>
                          set(
                            "task_ids",
                            form.task_ids.filter((value) => value !== id),
                          )
                        }
                      >
                        #{id} · {t("移除")}
                      </Button>
                    ))}
                  </div>
                </div>
              )}
            </div>
          </fieldset>
          {(error ?? validation) && (
            <p role="alert" className="text-destructive text-sm">
              {error ?? t(validation ?? "")}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" disabled={busy} onClick={onClose}>
              {t("取消")}
            </Button>
            <Button type="submit" disabled={busy || !!tasksError || unavailable.length > 0}>
              {t(busy ? "保存中…" : "保存计划")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
