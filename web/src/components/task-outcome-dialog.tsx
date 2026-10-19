"use client";

import * as React from "react";

import { BarChart3Icon } from "lucide-react";

import { TaskOutcomePanel } from "@/components/task-outcome-panel";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { useI18n } from "@/lib/i18n";

export function TaskOutcomeDialog({ taskId, taskName }: { taskId: string; taskName: string }) {
  const { t: uiText } = useI18n();
  const [open, setOpen] = React.useState(false);
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="icon" variant="ghost" aria-label={uiText("查看任务结果")} title={uiText("查看任务结果")}>
          <BarChart3Icon />
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-4xl">
        <DialogHeader>
          <DialogTitle>
            {uiText("任务结果")} · #{taskId}
          </DialogTitle>
          <DialogDescription className="break-words">{taskName}</DialogDescription>
        </DialogHeader>
        {open && <TaskOutcomePanel key={taskId} taskId={taskId} />}
      </DialogContent>
    </Dialog>
  );
}
