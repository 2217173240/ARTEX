import type { TaskOutcome } from "@/lib/types";

export type OutcomeMetric =
  | "findings.total"
  | "findings.statuses"
  | "findings.confirmed_severity"
  | "retests"
  | "tokens"
  | "tool_calls"
  | "skill_misses"
  | "wall_seconds";

export function outcomeAvailable(outcome: TaskOutcome, metric: OutcomeMetric): boolean {
  return !outcome.unavailable.some((key) => key === metric || metric.startsWith(`${key}.`));
}

export function outcomeDuration(seconds: number | null): string {
  if (seconds === null || !Number.isFinite(seconds) || seconds < 0) return "—";
  const rounded = Math.floor(seconds);
  const hours = Math.floor(rounded / 3600);
  const minutes = Math.floor((rounded % 3600) / 60);
  const rest = rounded % 60;
  return hours > 0
    ? `${hours}:${String(minutes).padStart(2, "0")}:${String(rest).padStart(2, "0")}`
    : `${minutes}:${String(rest).padStart(2, "0")}`;
}
