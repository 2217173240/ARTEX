import type { Activity } from "./types";

type HistoryPage = { items: Activity[]; earliestCursor: number; hasMore: boolean };

// Read the entire gap before publishing it. Publishing only the latest page would
// advance the next poll's anchor past missing rows during a long SSE outage.
export async function readActivityHistoryGap(
  fetchPage: (before: number) => Promise<HistoryPage>,
  since: number,
  isCurrent: () => boolean,
): Promise<Activity[] | undefined> {
  const bySeq = new Map<number, Activity>();
  let before = 0;
  while (isCurrent()) {
    const page = await fetchPage(before);
    if (!isCurrent()) return undefined;
    for (const item of page.items) {
      if (item.seq > since) bySeq.set(item.seq, item);
    }
    if (!page.hasMore || page.earliestCursor <= since) {
      return [...bySeq.values()].sort((left, right) => left.seq - right.seq);
    }
    if (page.earliestCursor <= 0 || (before > 0 && page.earliestCursor >= before)) {
      throw new Error("Session activity history cursor did not advance");
    }
    before = page.earliestCursor;
  }
  return undefined;
}

// A timeout after completion serializes polls, even when recovering many pages.
// Cancellation also discards requests already in flight (the API has no abort
// parameter) and prevents further pages, merges, and retries from that scope.
export function startActivityFallback({
  read,
  merge,
  isCurrent,
}: {
  read: (isCurrent: () => boolean) => Promise<Activity[] | undefined>;
  merge: (items: Activity[]) => void;
  isCurrent: () => boolean;
}): () => void {
  let cancelled = false;
  let timer: ReturnType<typeof setTimeout>;
  const current = () => !cancelled && isCurrent();
  const poll = async () => {
    try {
      if (!current()) return;
      const items = await read(current);
      if (current() && items?.length) merge(items);
    } catch {
      // Best effort: retain the previous anchor and retry the whole gap next tick.
    } finally {
      if (current()) timer = setTimeout(() => void poll(), 2000);
    }
  };
  timer = setTimeout(() => void poll(), 2000);
  return () => {
    cancelled = true;
    clearTimeout(timer);
  };
}
