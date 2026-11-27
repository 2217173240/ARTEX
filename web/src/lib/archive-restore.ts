import { ApiError } from "@/lib/api";

/**
 * Returns true when polling a restoring archive reports HTTP 404 (meaning the
 * archive finished restoring and was removed), independent of UI locale or message copy.
 */
export function isArchiveRestoreMissingError(error: unknown): boolean {
  if (error instanceof ApiError) return error.status === 404;
  return error instanceof Error && "status" in error && (error as { status?: unknown }).status === 404;
}
