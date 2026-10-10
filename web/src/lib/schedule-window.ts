/** Calendar previews mirror the server: skip missing wall times and use the earliest DST-fold instant. */
export interface ScheduleInput {
  name: string;
  enabled: boolean;
  type: "once" | "weekly";
  timezone: string;
  run_date: string;
  end_date: string;
  weekdays: number[];
  start_time: string;
  end_time: string;
  task_ids: string[];
}

export interface ScheduleWindow {
  start: string;
  end: string;
  active: boolean;
}

export const WEEKDAY_LABELS = ["周一", "周二", "周三", "周四", "周五", "周六", "周日"];

const formatters = new Map<string, Intl.DateTimeFormat>();
const resolvedInstants = new Map<string, number | null>();

export function localClock(now: Date, timezone: string): string {
  let formatter = formatters.get(timezone);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat("en-CA", {
      timeZone: timezone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hourCycle: "h23",
    });
    formatters.set(timezone, formatter);
  }
  const parts = formatter.formatToParts(now);
  const value = (key: string) => parts.find((part) => part.type === key)?.value ?? "";
  return `${value("year")}-${value("month")}-${value("day")} ${value("hour")}:${value("minute")}:${value("second")}`;
}

function addDays(day: string, days: number): string {
  const date = new Date(`${day}T00:00:00Z`);
  date.setUTCDate(date.getUTCDate() + days);
  return date.toISOString().slice(0, 10);
}

/** UTC milliseconds for the earliest exact wall-time match, or null for a DST gap. */
export function civilInstant(day: string, time: string, timezone: string): number | null {
  if (!validDate(day) || !/^([01]\d|2[0-3]):[0-5]\d$/.test(time)) return null;
  const key = `${timezone}|${day}|${time}`;
  if (resolvedInstants.has(key)) return resolvedInstants.get(key) ?? null;
  const expected = `${day} ${time}`;
  const seed = Date.parse(`${day}T${time}:00Z`);
  const offsets = new Set<number>();
  try {
    // Sample the timezone on both sides of a transition, matching the backend resolver.
    for (let hour = -48; hour <= 48; hour += 6) {
      const sample = seed + hour * 60 * 60 * 1000;
      const local = localClock(new Date(sample), timezone);
      offsets.add(Date.parse(`${local.replace(" ", "T")}Z`) - sample);
    }
    let first: number | null = null;
    for (const offset of offsets) {
      const candidate = seed - offset;
      if (localClock(new Date(candidate), timezone).slice(0, 16) !== expected) continue;
      if (first === null || candidate < first) first = candidate;
    }
    if (resolvedInstants.size >= 4096) resolvedInstants.clear();
    resolvedInstants.set(key, first);
    return first;
  } catch {
    return null;
  }
}

function occurrence(schedule: ScheduleInput, startDay: string, endDay: string) {
  const startInstant = civilInstant(startDay, schedule.start_time, schedule.timezone);
  const endInstant = civilInstant(endDay, schedule.end_time, schedule.timezone);
  if (startInstant === null || endInstant === null || endInstant <= startInstant) return null;
  return {
    start: `${startDay} ${schedule.start_time}`,
    end: `${endDay} ${schedule.end_time}`,
    startInstant,
    endInstant,
  };
}

function weeklyOccurrence(schedule: ScheduleInput, startDay: string) {
  const weekday = new Date(`${startDay}T00:00:00Z`).getUTCDay() || 7;
  if (!schedule.weekdays.includes(weekday)) return null;
  const endDay = schedule.end_time < schedule.start_time ? addDays(startDay, 1) : startDay;
  return occurrence(schedule, startDay, endDay);
}

export function scheduleWindow(schedule: ScheduleInput, now: Date = new Date()): ScheduleWindow | null {
  if (!schedule.enabled) return null;
  const instant = now.getTime();
  try {
    const day = localClock(now, schedule.timezone).slice(0, 10);
    const windows =
      schedule.type === "once"
        ? [occurrence(schedule, schedule.run_date, schedule.end_date || schedule.run_date)]
        : // Two weeks reaches the next valid weekly occurrence after a DST gap.
          Array.from({ length: 16 }, (_, index) => weeklyOccurrence(schedule, addDays(day, index - 1)));
    for (const window of windows) {
      if (window && instant < window.endInstant) {
        return { start: window.start, end: window.end, active: instant >= window.startInstant };
      }
    }
  } catch {
    return null;
  }
  return null;
}

/** Calendar day membership uses valid concrete occurrences, so skipped DST windows never appear. */
export function scheduleOccursOnDay(schedule: ScheduleInput, day: string): boolean {
  if (!schedule.enabled || !validDate(day)) return false;
  try {
    const windows =
      schedule.type === "once"
        ? [occurrence(schedule, schedule.run_date, schedule.end_date || schedule.run_date)]
        : [weeklyOccurrence(schedule, addDays(day, -1)), weeklyOccurrence(schedule, day)];
    return windows.some(
      (window) => window !== null && window.start < `${addDays(day, 1)} 00:00` && window.end > `${day} 00:00`,
    );
  } catch {
    return false;
  }
}

function validDate(value: string): boolean {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const date = new Date(`${value}T00:00:00Z`);
  return Number.isFinite(date.getTime()) && date.toISOString().slice(0, 10) === value;
}

/** Returns application-owned translation keys. Server remains authoritative. */
export function validateSchedule(input: ScheduleInput): string | null {
  if (!input.name.trim() || new TextEncoder().encode(input.name.trim()).length > 200) {
    return "请填写计划名称（最多 200 字节）";
  }
  if (!input.timezone.trim() || ["Local", "system"].includes(input.timezone)) return "请输入有效的 IANA 时区";
  try {
    new Intl.DateTimeFormat("en", { timeZone: input.timezone }).format();
  } catch {
    return "请输入有效的 IANA 时区";
  }
  if (![input.start_time, input.end_time].every((time) => /^([01]\d|2[0-3]):[0-5]\d$/.test(time))) {
    return "请填写有效的开始和结束时间";
  }
  if (input.type === "once") {
    if (!validDate(input.run_date) || !validDate(input.end_date)) return "请选择有效的开始和结束日期";
    if (`${input.end_date} ${input.end_time}` <= `${input.run_date} ${input.start_time}`)
      return "结束时间必须晚于开始时间";
    const start = civilInstant(input.run_date, input.start_time, input.timezone);
    const end = civilInstant(input.end_date, input.end_time, input.timezone);
    if (start === null || end === null) return "所选时间在该时区不存在，请避开夏令时跳转";
    if (end <= start) return "结束时间必须晚于开始时间";
  } else if (!input.weekdays.length || input.weekdays.some((day) => !Number.isInteger(day) || day < 1 || day > 7)) {
    return "请至少选择一个星期";
  } else if (input.start_time === input.end_time) {
    return "开始和结束时间不能相同";
  }
  if (!input.task_ids.length || input.task_ids.length > 100) return "请选择 1 至 100 个任务";
  return null;
}

export function isSchedulableTask(task: { status: string }): boolean {
  return !["done", "failed", "timeout"].includes(task.status);
}

/** Whitelist the write contract; read-only response fields never reach POST/PATCH. */
export function schedulePayload(input: ScheduleInput): ScheduleInput {
  return {
    name: input.name.trim(),
    enabled: input.enabled,
    type: input.type,
    timezone: input.timezone.trim(),
    run_date: input.type === "once" ? input.run_date : "",
    end_date: input.type === "once" ? input.end_date : "",
    weekdays: input.type === "weekly" ? [...input.weekdays] : [],
    start_time: input.start_time,
    end_time: input.end_time,
    task_ids: [...input.task_ids],
  };
}
