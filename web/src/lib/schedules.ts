import { http } from "@/lib/api";
import { MOCK } from "@/lib/mock/enabled";
import { type ScheduleInput, schedulePayload } from "@/lib/schedule-window";

export type { ScheduleInput } from "@/lib/schedule-window";
export interface TaskSchedule extends ScheduleInput {
  id: number;
  created_at: string;
  updated_at: string;
  manual_until?: string | null;
  next_start?: string | null;
  next_end?: string | null;
  status?: "running" | "scheduled" | "paused" | "expired";
}
export interface ScheduleRun {
  id: number;
  task_id: string;
  action: string;
  status: string;
  error?: string;
  created_at: string;
}
export interface ScheduleDetail {
  schedule: TaskSchedule;
  history: ScheduleRun[];
}
export interface ScheduleRunResult {
  id: string;
  ok: boolean;
  status?: "running" | "queued";
  queued?: boolean;
  error?: string;
}

// Preview data stays in this browser session and never calls task admission.
let previewSchedules: TaskSchedule[] = [];
let previewID = 1;
function write<T>(path: string, method: string, body?: unknown) {
  return http<T>(path, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body ?? {}) });
}
function previewSave(input: ScheduleInput, id?: number): TaskSchedule {
  const now = new Date().toISOString();
  const previous = previewSchedules.find((row) => row.id === id);
  const row = { ...input, id: id ?? previewID++, created_at: previous?.created_at ?? now, updated_at: now };
  previewSchedules = [...previewSchedules.filter((item) => item.id !== row.id), row];
  return row;
}

export const schedulesApi = {
  list: async (): Promise<TaskSchedule[]> => {
    if (MOCK) return structuredClone(previewSchedules);
    const response = await http<{ schedules: TaskSchedule[] | null }>("/schedules");
    return response.schedules ?? [];
  },
  detail: async (id: number): Promise<ScheduleDetail> => {
    if (MOCK) {
      const row = previewSchedules.find((item) => item.id === id);
      if (!row) throw new Error("Schedule not found");
      return { schedule: structuredClone(row), history: [] };
    }
    const response = await http<ScheduleDetail>(`/schedules/${id}`);
    return { ...response, history: response.history ?? [] };
  },
  create: async (input: ScheduleInput): Promise<TaskSchedule> =>
    MOCK ? previewSave(input) : write<TaskSchedule>("/schedules", "POST", schedulePayload(input)),
  update: async (id: number, input: ScheduleInput): Promise<TaskSchedule> =>
    MOCK ? previewSave(input, id) : write<TaskSchedule>(`/schedules/${id}`, "PATCH", schedulePayload(input)),
  setEnabled: async (row: TaskSchedule, enabled: boolean): Promise<TaskSchedule> =>
    MOCK
      ? previewSave({ ...row, enabled }, row.id)
      : write<TaskSchedule>(`/schedules/${row.id}/${enabled ? "resume" : "pause"}`, "POST"),
  remove: async (id: number): Promise<void> => {
    if (MOCK) previewSchedules = previewSchedules.filter((row) => row.id !== id);
    else await write<{ ok: boolean }>(`/schedules/${id}`, "DELETE");
  },
  runNow: async (id: number): Promise<{ results: ScheduleRunResult[]; manual_until?: string | null }> => {
    if (MOCK) throw new Error("Preview cannot start tasks");
    return write<{ results: ScheduleRunResult[]; manual_until?: string | null }>(`/schedules/${id}/run-now`, "POST");
  },
};
