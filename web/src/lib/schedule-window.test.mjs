import {
  civilInstant,
  isSchedulableTask,
  scheduleOccursOnDay,
  schedulePayload,
  scheduleWindow,
  validateSchedule,
} from "./schedule-window.ts";
import assert from "node:assert/strict";
import test from "node:test";

const input = (extra = {}) => ({
  name: "Window",
  enabled: true,
  type: "weekly",
  timezone: "Asia/Shanghai",
  run_date: "",
  end_date: "",
  weekdays: [1],
  start_time: "09:00",
  end_time: "18:00",
  task_ids: ["1"],
  ...extra,
});

test("weekly preview follows explicit timezone and half-open boundaries", () => {
  assert.equal(scheduleWindow(input(), new Date("2026-10-12T01:00:00Z")).active, true);
  const next = scheduleWindow(input(), new Date("2026-10-12T10:00:00Z"));
  assert.equal(next.active, false);
  assert.equal(next.start, "2026-10-19 09:00");
});
test("overnight weekly windows enter on selected weekday and exit next day", () => {
  const row = input({ weekdays: [7], start_time: "22:00", end_time: "02:00" });
  const active = scheduleWindow(row, new Date("2026-10-11T17:00:00Z"));
  assert.deepEqual(active, { start: "2026-10-11 22:00", end: "2026-10-12 02:00", active: true });
  assert.equal(scheduleWindow(row, new Date("2026-10-11T18:00:00Z")).start, "2026-10-18 22:00");
});
test("multi-day once is continuous and expires at its local end", () => {
  const row = input({ type: "once", run_date: "2026-10-10", end_date: "2026-10-13" });
  assert.equal(scheduleWindow(row, new Date("2026-10-11T20:00:00Z")).active, true);
  assert.equal(scheduleWindow(row, new Date("2026-10-13T10:00:00Z")), null);
  assert.equal(scheduleWindow({ ...row, enabled: false }), null);
});
test("a window ending after the fold remains open through both repeated-hour copies", () => {
  const row = input({ timezone: "America/New_York", weekdays: [7], start_time: "01:00", end_time: "02:00" });
  assert.equal(scheduleWindow(row, new Date("2026-11-01T05:30:00Z")).active, true);
  assert.equal(scheduleWindow(row, new Date("2026-11-01T06:30:00Z")).active, true);
});
test("validation rejects impossible dates, zero windows, invalid zones and task limits", () => {
  assert.equal(validateSchedule(input()), null);
  for (const extra of [
    { timezone: "Local" },
    { timezone: "Invalid/Zone" },
    { weekdays: [] },
    { weekdays: [8] },
    { start_time: "9:00" },
    { end_time: "09:00" },
    { task_ids: [] },
    { task_ids: Array.from({ length: 101 }, (_, i) => String(i + 1)) },
    { type: "once", run_date: "2026-02-30", end_date: "2026-03-01" },
    { type: "once", run_date: "2026-10-10", end_date: "2026-10-09" },
  ]) {
    assert.ok(validateSchedule(input(extra)), JSON.stringify(extra));
  }
  assert.ok(validateSchedule(input({ name: "中".repeat(67) })));
});
test("terminal tasks are excluded while human-paused tasks remain selectable", () => {
  for (const status of ["done", "failed", "timeout"]) assert.equal(isSchedulableTask({ status }), false);
  for (const status of ["created", "queued", "running", "paused"]) assert.equal(isSchedulableTask({ status }), true);
});

test("writes whitelist input and remove response and inactive recurrence fields", () => {
  const payload = schedulePayload(
    input({ id: 10, next_start: "computed", manual_until: "computed", run_date: "2026-10-10", end_date: "2026-10-11" }),
  );
  assert.equal(payload.id, undefined);
  assert.equal(payload.next_start, undefined);
  assert.equal(payload.manual_until, undefined);
  assert.equal(payload.run_date, "");
  assert.equal(payload.end_date, "");
  assert.deepEqual(schedulePayload(input({ type: "once", weekdays: [1] })).weekdays, []);
});

test("a weekly nonexistent spring-forward start is skipped until the following week", () => {
  const row = input({ timezone: "America/New_York", weekdays: [7], start_time: "02:30", end_time: "04:00" });
  assert.equal(civilInstant("2026-03-08", "02:30", row.timezone), null);
  assert.deepEqual(scheduleWindow(row, new Date("2026-03-08T07:00:00Z")), {
    start: "2026-03-15 02:30",
    end: "2026-03-15 04:00",
    active: false,
  });
  assert.equal(scheduleOccursOnDay(row, "2026-03-08"), false);
  assert.equal(scheduleOccursOnDay(row, "2026-03-15"), true);
});
test("a repeated-hour window uses the earliest occurrence and stays closed in the second fold", () => {
  const row = input({ timezone: "America/New_York", weekdays: [7], start_time: "01:15", end_time: "01:45" });
  assert.equal(civilInstant("2026-11-01", "01:15", row.timezone), Date.parse("2026-11-01T05:15:00Z"));
  assert.equal(scheduleWindow(row, new Date("2026-11-01T05:30:00Z")).active, true);
  assert.equal(scheduleWindow(row, new Date("2026-11-01T05:45:00Z")).active, false);
  assert.deepEqual(scheduleWindow(row, new Date("2026-11-01T06:30:00Z")), {
    start: "2026-11-08 01:15",
    end: "2026-11-08 01:45",
    active: false,
  });
  assert.equal(scheduleOccursOnDay(row, "2026-11-01"), true);
});
test("once validation and calendar reject nonexistent boundaries and honor midnight ends", () => {
  const row = input({
    type: "once",
    timezone: "America/New_York",
    run_date: "2026-03-08",
    end_date: "2026-03-08",
    start_time: "01:00",
    end_time: "02:30",
  });
  assert.equal(validateSchedule(row), "所选时间在该时区不存在，请避开夏令时跳转");
  assert.equal(scheduleWindow(row, new Date("2026-03-08T06:30:00Z")), null);
  assert.equal(scheduleOccursOnDay(row, "2026-03-08"), false);
  const overnight = input({ weekdays: [7], start_time: "22:00", end_time: "02:00" });
  assert.equal(scheduleOccursOnDay(overnight, "2026-10-11"), true);
  assert.equal(scheduleOccursOnDay(overnight, "2026-10-12"), true);
  assert.equal(scheduleOccursOnDay({ ...overnight, end_time: "00:00" }, "2026-10-12"), false);
});
