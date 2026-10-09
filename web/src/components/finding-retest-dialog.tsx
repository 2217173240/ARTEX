"use client";

import { useI18n } from "@/lib/i18n";

import * as React from "react";

import { RotateCcwIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { FindingRetest } from "@/lib/types";

interface FindingRetestDialogProps {
  findingId: string;
  findingName?: string;
  onClose: () => void;
  onStarted?: (retest: FindingRetest) => void;
}

// 仅在打开时挂载，关闭后清空说明；列表与详情共用提交锁及错误处理，启动后留在当前页。
export function FindingRetestDialog({ findingId, findingName, onClose, onStarted }: FindingRetestDialogProps) {
  const { t: uiText } = useI18n();
  const notesId = React.useId();
  const [notes, setNotes] = React.useState("");
  const [submitting, setSubmitting] = React.useState(false);
  const submitLock = React.useRef(false);

  async function start() {
    if (submitLock.current) return;
    submitLock.current = true;
    setSubmitting(true);
    try {
      const result = await api.startFindingRetest(findingId, notes.trim());
      onStarted?.(result.retest);
      onClose();
      toast.success(
        result.created ? uiText("复测已启动，可点击「复测中」查看会话") : uiText("该漏洞正在复测，可查看已有会话"),
      );
    } catch (e) {
      toast.error(uiText("发起复测失败：{v0}", { v0: (e as Error).message }));
    } finally {
      submitLock.current = false;
      setSubmitting(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && !submitLock.current && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {uiText("复测漏洞 #")}
            {findingId}
          </DialogTitle>
          <DialogDescription className="break-words">
            {findingName ? <span className="mb-2 block">{findingName}</span> : null}
            {uiText(
              "复测 Agent 将读取原证据和测试约束，在独立会话中执行针对性验证。复测成功完成且确认修复后，漏洞状态自动改为「已修复」，其他结论保留原状态。",
            )}
          </DialogDescription>
        </DialogHeader>
        <FieldGroup>
          <Field data-disabled={submitting}>
            <FieldLabel htmlFor={notesId}>{uiText("补充说明（可选）")}</FieldLabel>
            <Textarea
              id={notesId}
              value={notes}
              maxLength={4000}
              rows={4}
              disabled={submitting}
              onChange={(e) => setNotes(e.target.value)}
              placeholder={uiText("例如：使用原测试账号验证原接口；修复版本为 v2。")}
            />
            <FieldDescription>{uiText("可补充修复版本、测试条件或本次限制。")}</FieldDescription>
          </Field>
        </FieldGroup>
        <DialogFooter>
          <Button variant="outline" disabled={submitting} onClick={onClose}>
            {uiText("取消")}
          </Button>
          <Button disabled={submitting} onClick={() => void start()}>
            {submitting ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}
            {submitting ? uiText("正在创建…") : uiText("开始复测")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
