"use client";

import * as React from "react";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";
import {
  AlertDialog, AlertDialogTrigger, AlertDialogContent, AlertDialogHeader,
  AlertDialogTitle, AlertDialogDescription, AlertDialogFooter,
  AlertDialogCancel, AlertDialogAction,
} from "@/components/ui/alert-dialog";

/** The caller reports errors and returns false to keep the confirmation retryable. */
export function DeleteConfirmation({ name, description, onConfirm, children }: {
  name: string;
  description: string;
  onConfirm: () => Promise<boolean>;
  children: React.ReactElement;
}) {
  const { t } = useI18n();
  const [open, setOpen] = React.useState(false);
  const [pending, setPending] = React.useState(false);
  const inFlight = React.useRef(false);
  const cancelRef = React.useRef<HTMLButtonElement>(null);

  async function confirm() {
    if (inFlight.current) return;
    inFlight.current = true;
    setPending(true);
    try {
      if (await onConfirm()) setOpen(false);
    } catch {
      toast.error(t("删除失败，请重试。"));
    } finally {
      inFlight.current = false;
      setPending(false);
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={(next) => { if (!inFlight.current) setOpen(next); }}>
      <AlertDialogTrigger asChild>{children}</AlertDialogTrigger>
      <AlertDialogContent
        aria-busy={pending}
        onClick={(event) => event.stopPropagation()}
        onOpenAutoFocus={(event) => { event.preventDefault(); cancelRef.current?.focus(); }}
        onEscapeKeyDown={(event) => { if (inFlight.current) event.preventDefault(); }}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>{t("确认删除？")}</AlertDialogTitle>
          <AlertDialogDescription className="break-words">
            {t("将永久删除“{name}”。此操作无法撤销。", { name })} {description}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel ref={cancelRef} disabled={pending}>{t("取消")}</AlertDialogCancel>
          <AlertDialogAction variant="destructive" disabled={pending} onClick={(event) => {
            event.preventDefault();
            // Each caller handles API failures using its existing error notification.
            void confirm();
          }}>{pending ? t("正在删除…") : t("确认删除")}</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
