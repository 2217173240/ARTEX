type SyncResult = {
  status: "success" | "partial" | "failed";
  truncated: boolean;
  synced: Record<string, number> | null;
  warnings: string[] | null;
  errors: string[] | null;
};

/** One presentation for the notification and the persistent result card. */
export function syncOutcome(result: SyncResult) {
  const total = Object.values(result.synced ?? {}).reduce((sum, count) => sum + count, 0);
  if (result.status === "failed") return { severity: "error", title: "同步失败", total } as const;
  if (result.status !== "success" && result.status !== "partial") {
    return { severity: "warning", title: "同步结果未确认", total } as const;
  }
  // Missing completeness metadata and contradictory success responses must not
  // turn a legacy/invalid response into a successful completion notification.
  if (typeof result.truncated !== "boolean") {
    return { severity: "warning", title: "同步结果未确认", total } as const;
  }
  if (result.status === "partial" || result.truncated || result.warnings?.length || result.errors?.length) {
    return {
      severity: result.errors?.length && total === 0 ? "error" : "warning",
      title: result.errors?.length && total === 0 ? "同步失败" : "同步部分完成",
      total,
    } as const;
  }
  return { severity: "success", title: "同步完成", total } as const;
}
