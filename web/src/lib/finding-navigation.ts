const TASK_TABS = [
  "sessions",
  "overview",
  "graph",
  "evidence",
  "broadcast",
  "findings",
  "retests",
  "assets",
  "coverage",
  "intercept",
  "report",
] as const;

export function taskTab(value: string | null): string {
  return TASK_TABS.find((tab) => tab === value) ?? "sessions";
}

export function taskTabHref(query: string, value: string): string {
  const params = new URLSearchParams(query);
  params.set("tab", taskTab(value));
  return `/function/tasks/detail?${params.toString()}`;
}

export function returnTaskId(value: string | null | undefined): string | undefined {
  if (!value || !/^[1-9]\d{0,18}$/.test(value)) return undefined;
  const id = BigInt(value);
  return id <= BigInt("9223372036854775807") ? value : undefined;
}

export function findingReturnHref(task: string | null | undefined): string {
  const id = returnTaskId(task);
  return id ? taskTabHref(new URLSearchParams({ id }).toString(), "findings") : "/function/findings";
}

export function findingReportHref(
  kind: "detail" | "case",
  id: string,
  contextTask?: string,
  returnTask?: string,
): string {
  const params = new URLSearchParams({ id });
  if (contextTask) params.set("context_task", contextTask);
  const origin = returnTaskId(returnTask);
  if (origin) params.set("return_task", origin);
  return `/function/findings/${kind}?${params.toString()}`;
}
