"use client";

import { useI18n } from "@/lib/i18n";

import * as React from "react";

import { CheckIcon, CopyIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { cn, copyText } from "@/lib/utils";

type CopyButtonProps = {
  // 要复制的文本;为空则按钮禁用。
  text: string | null | undefined;
  // 复制成功后的 toast 文案,默认「已复制」。
  successMessage?: string;
  label?: React.ReactNode;
  size?: React.ComponentProps<typeof Button>["size"];
  variant?: React.ComponentProps<typeof Button>["variant"];
  className?: string;
};

// CopyButton 统一的「复制到剪贴板」按钮:内置成功/失败反馈,并在 HTTP 非安全上下文
// 下自动降级(见 copyText)。
export function CopyButton({
  text,
  successMessage: successMessageProp,
  label: labelProp,
  size = "sm",
  variant = "outline",
  className,
}: CopyButtonProps) {
  const { t: uiText } = useI18n();
  const successMessage = successMessageProp ?? uiText("已复制");
  const label = labelProp ?? uiText("复制");
  const [copied, setCopied] = React.useState(false);
  const timer = React.useRef<ReturnType<typeof setTimeout> | null>(null);

  React.useEffect(() => {
    return () => {
      if (timer.current) clearTimeout(timer.current);
    };
  }, []);

  async function handleCopy() {
    if (!text) return;
    const ok = await copyText(text);
    if (ok) {
      setCopied(true);
      toast.success(successMessage);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1500);
    } else {
      toast.error(uiText("复制失败，请手动选择文本复制"));
    }
  }

  return (
    <Button type="button" size={size} variant={variant} className={cn(className)} disabled={!text} onClick={handleCopy}>
      {copied ? <CheckIcon /> : <CopyIcon />}
      {label}
    </Button>
  );
}
