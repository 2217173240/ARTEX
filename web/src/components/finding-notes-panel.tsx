"use client";

import * as React from "react";

import { Trash2Icon } from "lucide-react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import {
  FINDING_NOTE_LIMIT,
  type FindingNote,
  type FindingNotePage,
  findingNoteLength,
  findingNotes,
} from "@/lib/finding-notes";
import { useI18n } from "@/lib/i18n";
import { MOCK } from "@/lib/mock/enabled";
import { statusMeta } from "@/lib/status";
import type { Finding } from "@/lib/types";

export function FindingLastReview({ finding }: { finding: Finding }) {
  const { t, locale } = useI18n();
  if (!finding.reviewed_by || !finding.reviewed_at || !finding.reviewed_status) return null;
  return (
    <p className="whitespace-normal text-muted-foreground text-xs">
      {t("最后人工处置：共享 ARTEX 管理员于 {v0} 标记为「{v1}」", {
        v0: new Date(finding.reviewed_at).toLocaleString(locale),
        v1: t(statusMeta("finding", finding.reviewed_status).label),
      })}
    </p>
  );
}

type NotesProps = { findingId: string; contextTask?: string; readOnly?: boolean };

// Changing finding/context remounts state and invalidates outstanding work.
export function FindingNotesPanel(props: NotesProps) {
  return <FindingNotesInner key={JSON.stringify([props.findingId, props.contextTask])} {...props} />;
}

function FindingNotesInner({ findingId, contextTask, readOnly = false }: NotesProps) {
  const { t, locale } = useI18n();
  const [page, setPage] = React.useState<FindingNotePage | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [loadError, setLoadError] = React.useState("");
  const [mutationError, setMutationError] = React.useState("");
  const [failedBefore, setFailedBefore] = React.useState<number | undefined>();
  const [body, setBody] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const requestVersion = React.useRef(0);
  const inputId = React.useId();

  const load = React.useCallback(
    async (before?: number) => {
      const version = ++requestVersion.current;
      setLoading(true);
      setLoadError("");
      try {
        const next = await findingNotes.list(findingId, contextTask, before);
        if (version !== requestVersion.current) return;
        setPage((current) => ({
          ...next,
          notes:
            before === undefined
              ? next.notes
              : [
                  ...(current?.notes ?? []),
                  ...next.notes.filter((note) => !current?.notes.some((old) => old.id === note.id)),
                ],
        }));
      } catch (error) {
        if (version !== requestVersion.current) return;
        setFailedBefore(before);
        setLoadError((error as Error).message);
      } finally {
        if (version === requestVersion.current) setLoading(false);
      }
    },
    [findingId, contextTask],
  );

  React.useEffect(() => {
    void load();
    return () => {
      requestVersion.current++;
    };
  }, [load]);

  const create = async (event: React.FormEvent) => {
    event.preventDefault();
    if (readOnly || busy || loading || !page || !body.trim() || findingNoteLength(body) > FINDING_NOTE_LIMIT) return;
    const version = ++requestVersion.current;
    setBusy(true);
    setMutationError("");
    try {
      const note = await findingNotes.create(findingId, body, contextTask);
      if (version !== requestVersion.current) return;
      setPage(
        (current) => current && { ...current, notes: [note, ...current.notes.filter((old) => old.id !== note.id)] },
      );
      setBody("");
    } catch (error) {
      if (version === requestVersion.current) setMutationError((error as Error).message);
    } finally {
      if (version === requestVersion.current) setBusy(false);
    }
  };

  const remove = async (note: FindingNote) => {
    if (readOnly || busy || loading) return;
    const version = ++requestVersion.current;
    setBusy(true);
    setMutationError("");
    try {
      await findingNotes.remove(findingId, note.id, contextTask);
      if (version !== requestVersion.current) return;
      setPage((current) => current && { ...current, notes: current.notes.filter((old) => old.id !== note.id) });
    } catch (error) {
      if (version === requestVersion.current) setMutationError((error as Error).message);
    } finally {
      if (version === requestVersion.current) setBusy(false);
    }
  };
  const length = findingNoteLength(body);

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("处置备注")}</CardTitle>
        <CardDescription>{t("记录补充证据与处理说明，备注以共享 ARTEX 管理员身份保存。")}</CardDescription>
        {MOCK ? <Badge variant="outline">{t("预览数据")}</Badge> : null}
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {loadError ? (
          <Alert variant="destructive">
            <AlertDescription>
              {t("加载备注失败：{v0}", { v0: loadError })}
              <Button variant="outline" size="sm" disabled={loading || busy} onClick={() => void load(failedBefore)}>
                {t("重试")}
              </Button>
            </AlertDescription>
          </Alert>
        ) : null}
        {mutationError ? (
          <Alert variant="destructive">
            <AlertDescription>{t("备注操作失败：{v0}", { v0: mutationError })}</AlertDescription>
          </Alert>
        ) : null}
        {page === null && !loadError ? <Skeleton className="h-20 w-full" /> : null}
        {page?.notes.length === 0 && !loadError ? (
          <p className="text-muted-foreground text-sm">{t("暂无处置备注")}</p>
        ) : null}
        {page?.notes.map((note) => (
          <article key={note.id} className="min-w-0 rounded-lg border p-3">
            <div className="mb-2 flex flex-wrap items-center gap-2 text-muted-foreground text-xs">
              <span>{note.author}</span>
              <time dateTime={note.created_at}>{new Date(note.created_at).toLocaleString(locale)}</time>
              {!readOnly ? (
                <AlertDialog>
                  <AlertDialogTrigger asChild>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="ml-auto size-7"
                      disabled={busy || loading}
                      aria-label={t("删除备注")}
                    >
                      <Trash2Icon aria-hidden="true" />
                    </Button>
                  </AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>{t("删除这条备注？")}</AlertDialogTitle>
                      <AlertDialogDescription>{t("删除后无法恢复。")}</AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>{t("取消")}</AlertDialogCancel>
                      <AlertDialogAction onClick={() => void remove(note)}>{t("删除")}</AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              ) : null}
            </div>
            <p className="whitespace-pre-wrap break-words text-sm">{note.body}</p>
          </article>
        ))}
        {page?.has_more ? (
          <Button
            variant="outline"
            disabled={loading || busy || page.next_before === undefined}
            onClick={() => void load(page.next_before)}
          >
            {loading ? <Spinner aria-hidden="true" /> : null}
            {t("加载更早备注")}
          </Button>
        ) : null}
        {!readOnly ? (
          <form onSubmit={create} className="flex flex-col gap-2">
            <Label htmlFor={inputId}>{t("添加备注")}</Label>
            <Textarea
              id={inputId}
              value={body}
              onChange={(event) => setBody(event.target.value)}
              disabled={busy}
              placeholder={t("填写补充证据或处理说明")}
              aria-describedby={`${inputId}-count`}
            />
            <div className="flex items-center justify-between gap-3">
              <span
                id={`${inputId}-count`}
                className={length > FINDING_NOTE_LIMIT ? "text-destructive text-xs" : "text-muted-foreground text-xs"}
              >
                {length} / {FINDING_NOTE_LIMIT}
              </span>
              <Button
                type="submit"
                disabled={busy || loading || page === null || !body.trim() || length > FINDING_NOTE_LIMIT}
              >
                {busy ? <Spinner aria-hidden="true" /> : null}
                {t("保存备注")}
              </Button>
            </div>
          </form>
        ) : null}
      </CardContent>
    </Card>
  );
}
