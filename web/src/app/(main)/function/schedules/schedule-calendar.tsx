"use client";

import { useMemo, useState } from "react";

import { ChevronLeftIcon, ChevronRightIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";
import { scheduleOccursOnDay, WEEKDAY_LABELS } from "@/lib/schedule-window";
import type { TaskSchedule } from "@/lib/schedules";
import { cn } from "@/lib/utils";

/** Dates here are civil dates; each plan's wall clock remains in its own timezone. */
export function ScheduleCalendar({
  schedules,
  onSelect,
  onCreate,
}: {
  schedules: TaskSchedule[];
  onSelect: (row: TaskSchedule) => void;
  onCreate: (day: string) => void;
}) {
  const { t, locale } = useI18n();
  const [month, setMonth] = useState(() => new Date(new Date().getFullYear(), new Date().getMonth(), 1));
  const [selectedDay, setSelectedDay] = useState<string | null>(null);
  const days = useMemo(() => {
    const start = new Date(Date.UTC(month.getFullYear(), month.getMonth(), 1));
    start.setUTCDate(start.getUTCDate() - ((start.getUTCDay() + 6) % 7));
    return Array.from({ length: 42 }, (_, index) => {
      const day = new Date(start);
      day.setUTCDate(day.getUTCDate() + index);
      return { date: day, key: day.toISOString().slice(0, 10) };
    });
  }, [month]);
  const onDay = (day: string) => schedules.filter((row) => scheduleOccursOnDay(row, day));
  const today = new Date().toLocaleDateString("en-CA");
  const selected = selectedDay ? onDay(selectedDay) : [];
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="font-semibold">{month.toLocaleDateString(locale, { year: "numeric", month: "long" })}</h2>
          <p className="text-muted-foreground text-xs">{t("日历按各计划的当地日期展示，时间使用计划时区")}</p>
        </div>
        <div className="flex items-center gap-1">
          <Button
            variant="outline"
            size="icon-sm"
            aria-label={t("上个月")}
            onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))}
          >
            <ChevronLeftIcon />
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setMonth(new Date(new Date().getFullYear(), new Date().getMonth(), 1));
              setSelectedDay(null);
            }}
          >
            {t("今天")}
          </Button>
          <Button
            variant="outline"
            size="icon-sm"
            aria-label={t("下个月")}
            onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))}
          >
            <ChevronRightIcon />
          </Button>
        </div>
      </div>
      <div className="grid grid-cols-7 overflow-hidden rounded-xl border">
        {WEEKDAY_LABELS.map((label) => (
          <div key={label} className="border-b bg-muted/40 p-2 text-center text-muted-foreground text-xs">
            {t(label)}
          </div>
        ))}
        {days.map(({ date, key }) => {
          const rows = onDay(key);
          const outside = date.getUTCMonth() !== month.getMonth();
          return (
            <div
              key={key}
              className={cn(
                "min-h-20 border-r border-b p-1 last:border-r-0 sm:min-h-28 sm:p-2",
                outside && "bg-muted/20 text-muted-foreground",
                selectedDay === key && "bg-primary/5",
              )}
            >
              <button
                type="button"
                onClick={() => setSelectedDay(key)}
                aria-label={t("查看 {date} 的计划", { date: key })}
                aria-pressed={selectedDay === key}
                className={cn(
                  "mb-1 flex size-7 items-center justify-center rounded-full text-xs hover:bg-muted focus-visible:outline-primary",
                  today === key && "bg-primary text-primary-foreground",
                )}
              >
                {date.getUTCDate()}
              </button>
              <div className="space-y-1">
                {rows.slice(0, 2).map((row) => (
                  <button
                    type="button"
                    key={row.id}
                    onClick={() => onSelect(row)}
                    className="block w-full truncate rounded bg-primary/10 px-1 py-0.5 text-left text-[10px] text-primary sm:text-xs"
                    title={`${row.name} · ${row.start_time}–${row.end_time} · ${row.timezone}`}
                  >
                    {row.name}
                  </button>
                ))}
                {rows.length > 2 && (
                  <button type="button" onClick={() => setSelectedDay(key)} className="text-muted-foreground text-xs">
                    +{rows.length - 2}
                  </button>
                )}
              </div>
            </div>
          );
        })}
      </div>
      {selectedDay && (
        <div className="rounded-lg border bg-muted/20 p-3">
          <div className="mb-2 flex items-center justify-between gap-3">
            <p className="font-medium text-sm">
              {selectedDay} · {t("{count} 个计划窗口", { count: selected.length })}
            </p>
            <Button variant="outline" size="sm" onClick={() => onCreate(selectedDay)}>
              {t("新增计划")}
            </Button>
          </div>
          <div className="flex flex-wrap gap-2">
            {selected.map((row) => (
              <Button
                key={row.id}
                variant="ghost"
                size="sm"
                onClick={() => onSelect(row)}
                className="h-auto whitespace-normal text-left"
              >
                {row.name} · {row.start_time}–{row.end_time} · {row.timezone}
              </Button>
            ))}
          </div>
          {!selected.length && <p className="text-muted-foreground text-sm">{t("这一天没有启用的计划窗口")}</p>}
        </div>
      )}
    </div>
  );
}
