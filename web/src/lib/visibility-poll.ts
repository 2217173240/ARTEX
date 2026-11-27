export interface VisibilityPollOptions {
  immediate?: boolean;
  isPaused?: () => boolean;
}

function isDocumentHidden(): boolean {
  return typeof document !== "undefined" && Boolean(document.hidden);
}

/**
 * Starts a visibility-aware, overlap-guarded periodic poll.
 * Skips interval ticks while the page is hidden or `isPaused()` is true,
 * and triggers one immediate guarded run when returning to visible.
 */
export function startVisibilityPoll(
  fn: () => void | Promise<void>,
  intervalMs: number,
  options?: VisibilityPollOptions,
): () => void {
  let stopped = false;
  let inFlight = false;

  const run = () => {
    if (stopped || inFlight) return;
    inFlight = true;
    try {
      const result = fn();
      if (result !== null && typeof result === "object" && typeof (result as PromiseLike<void>).then === "function") {
        void Promise.resolve(result)
          .catch(() => {
            // Callers handle transient errors; reset inFlight so future ticks can retry.
          })
          .finally(() => {
            inFlight = false;
          });
      } else {
        inFlight = false;
      }
    } catch {
      inFlight = false;
    }
  };

  const shouldSkip = () => stopped || isDocumentHidden() || Boolean(options?.isPaused?.());

  const onTick = () => {
    if (shouldSkip()) return;
    run();
  };

  const onVisibilityChange = () => {
    if (shouldSkip()) return;
    run();
  };

  if (options?.immediate !== false) {
    run();
  }

  const timer = setInterval(onTick, intervalMs);
  if (typeof document !== "undefined" && typeof document.addEventListener === "function") {
    document.addEventListener("visibilitychange", onVisibilityChange);
  }

  return () => {
    if (stopped) return;
    stopped = true;
    clearInterval(timer);
    if (typeof document !== "undefined" && typeof document.removeEventListener === "function") {
      document.removeEventListener("visibilitychange", onVisibilityChange);
    }
  };
}
