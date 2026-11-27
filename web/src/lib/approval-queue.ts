export interface ApprovalTimeoutSource {
  created_at?: string | null;
  timeout_enabled?: boolean | null;
  timeout_seconds?: number | null;
}

/**
 * Compute remaining whole seconds (rounded up) before an approval row times out.
 * Returns null when timeout is disabled, invalid, or lacks a valid creation timestamp.
 */
export function approvalRemainingSeconds(row: ApprovalTimeoutSource, nowMs: number = Date.now()): number | null {
  if (!row.timeout_enabled) return null;
  if (typeof row.timeout_seconds !== "number" || !Number.isFinite(row.timeout_seconds) || row.timeout_seconds <= 0) {
    return null;
  }
  if (!row.created_at) return null;
  const createdMs = Date.parse(row.created_at);
  if (!Number.isFinite(createdMs)) return null;
  const remainingMs = createdMs + row.timeout_seconds * 1000 - nowMs;
  if (remainingMs <= 0) return 0;
  return Math.ceil(remainingMs / 1000);
}

/**
 * Check whether an approval row currently has an in-flight decision request.
 */
export function isRowDeciding(decidingIds: ReadonlySet<number>, id: number): boolean {
  return decidingIds.has(id);
}

/**
 * Return a Set containing `id` for per-row pending decision tracking.
 */
export function addDecidingId(decidingIds: ReadonlySet<number>, id: number): Set<number> {
  if (decidingIds.has(id)) {
    return decidingIds instanceof Set ? decidingIds : new Set(decidingIds);
  }
  const next = new Set(decidingIds);
  next.add(id);
  return next;
}

/**
 * Return a Set with `id` removed after a per-row decision request settles.
 */
export function removeDecidingId(decidingIds: ReadonlySet<number>, id: number): Set<number> {
  if (!decidingIds.has(id)) {
    return decidingIds instanceof Set ? decidingIds : new Set(decidingIds);
  }
  const next = new Set(decidingIds);
  next.delete(id);
  return next;
}
