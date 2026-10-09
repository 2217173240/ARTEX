"use client";

import { useI18n } from "@/lib/i18n";

import * as React from "react";

import { Badge } from "@/components/ui/badge";
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";
import { Textarea } from "@/components/ui/textarea";
import type { ParsedCompanyScopeText } from "@/lib/company-scope";
import type { CompanyScopeKind } from "@/lib/types";

const SCOPE_KIND_LABELS: Record<CompanyScopeKind, string> = {
  domain: "域名",
  ip: "IP",
  cidr: "CIDR",
  icp: "ICP",
  keyword: "关键词",
};

export function ScopeTextEditor({
  id,
  value,
  onValueChange,
  parsed,
  label: labelProp,
  description: descriptionProp,
}: {
  id: string;
  value: string;
  onValueChange: (value: string) => void;
  parsed: ParsedCompanyScopeText;
  label?: string;
  description?: string;
}) {
  const { t: uiText } = useI18n();
  const label = labelProp ?? uiText("资产范围");
  const description = descriptionProp ?? uiText("每行一条，自动识别域名、IP、CIDR、ICP 备案和企业关键词。");
  const counts = React.useMemo(() => {
    const result = new Map<CompanyScopeKind, number>();
    for (const rule of parsed.rules) result.set(rule.kind, (result.get(rule.kind) ?? 0) + 1);
    return result;
  }, [parsed.rules]);

  return (
    <Field data-invalid={parsed.errors.length > 0}>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <FieldDescription>{description}</FieldDescription>
      <Textarea
        id={id}
        rows={8}
        value={value}
        aria-invalid={parsed.errors.length > 0}
        placeholder={uiText("example.com\n203.0.113.10\n198.51.100.0/24\n京ICP备12345678号-1\n企业名称关键词")}
        className="min-h-36 resize-y font-mono text-sm"
        onChange={(event) => onValueChange(event.target.value)}
      />
      {parsed.rules.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5 text-muted-foreground text-xs">
          <span>
            {uiText("已识别")} {parsed.rules.length} {uiText("条")}
          </span>
          {Object.entries(SCOPE_KIND_LABELS).map(([kind, kindLabel]) => {
            const count = counts.get(kind as CompanyScopeKind) ?? 0;
            return count > 0 ? (
              <Badge key={kind} variant="secondary" className="font-mono tabular-nums">
                {kindLabel} {count}
              </Badge>
            ) : null;
          })}
        </div>
      )}
      {parsed.errors.length > 0 && (
        <FieldError>
          {parsed.errors.slice(0, 5).map((item) => (
            <span key={`${item.line}-${item.error}`} className="block">
              {uiText("第")} {item.line} {uiText("行：")}
              {item.error}
            </span>
          ))}
          {parsed.errors.length > 5 && (
            <span className="block">
              {uiText("另有")} {parsed.errors.length - 5} {uiText("行错误")}
            </span>
          )}
        </FieldError>
      )}
    </Field>
  );
}
