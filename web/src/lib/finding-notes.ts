import { http } from "@/lib/api";
import { MOCK } from "@/lib/mock/enabled";

export interface FindingNote {
  id: string;
  author: string;
  body: string;
  created_at: string;
}

export interface FindingNotePage {
  notes: FindingNote[];
  has_more: boolean;
  next_before?: number;
}

export const FINDING_NOTE_LIMIT = 8000;
export function findingNoteLength(body: string): number {
  return Array.from(body).length;
}

function notesPath(findingId: string, contextTask?: string, before?: number): string {
  const query = new URLSearchParams({ limit: "50" });
  if (contextTask) query.set("context_task", contextTask);
  if (before !== undefined) query.set("before", String(before));
  return `/exploration/findings/${encodeURIComponent(findingId)}/notes?${query}`;
}

// Preview-only memory. These notes are synthetic and never represent a signed-in person.
const previewNotes = new Map<string, FindingNote[]>();
let previewId = 0;

export const findingNotes = {
  async list(findingId: string, contextTask?: string, before?: number): Promise<FindingNotePage> {
    if (!MOCK) return http<FindingNotePage>(notesPath(findingId, contextTask, before));
    const rows = (previewNotes.get(findingId) ?? []).filter((note) => before === undefined || Number(note.id) < before);
    const notes = rows.slice(0, 50);
    return { notes, has_more: rows.length > 50, next_before: notes.length ? Number(notes.at(-1)?.id) : undefined };
  },
  async create(findingId: string, body: string, contextTask?: string): Promise<FindingNote> {
    if (!MOCK)
      return http<FindingNote>(notesPath(findingId, contextTask), { method: "POST", body: JSON.stringify({ body }) });
    const note = {
      id: String(++previewId),
      author: "ARTEX (shared admin)",
      body,
      created_at: new Date().toISOString(),
    };
    previewNotes.set(findingId, [note, ...(previewNotes.get(findingId) ?? [])]);
    return note;
  },
  async remove(findingId: string, noteId: string, contextTask?: string): Promise<void> {
    if (!MOCK) {
      const query = contextTask ? `?context_task=${encodeURIComponent(contextTask)}` : "";
      return http<void>(
        `/exploration/findings/${encodeURIComponent(findingId)}/notes/${encodeURIComponent(noteId)}${query}`,
        { method: "DELETE" },
      );
    }
    previewNotes.set(
      findingId,
      (previewNotes.get(findingId) ?? []).filter((note) => note.id !== noteId),
    );
  },
};
