"use client";

import * as React from "react";

import { BarChart3Icon, RefreshCwIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { type OutcomeMetric, outcomeAvailable, outcomeDuration } from "@/lib/task-outcome";
import type { TaskOutcome } from "@/lib/types";

export function TaskOutcomePanel({ taskId }: { taskId: string }) {
  const { t: uiText, locale } = useI18n();
  const [outcome, setOutcome] = React.useState<TaskOutcome | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  const requestVersion = React.useRef(0);

  const load = React.useCallback(() => {
    const version = ++requestVersion.current;
    setLoading(true);
    setError("");
    api
      .taskOutcome(taskId)
      .then((data) => {
        if (version === requestVersion.current) setOutcome(data);
      })
      .catch((reason: unknown) => {
        if (version === requestVersion.current) setError(reason instanceof Error ? reason.message : String(reason));
      })
      .finally(() => {
        if (version === requestVersion.current) setLoading(false);
      });
  }, [taskId]);

  React.useEffect(() => {
    load();
    return () => {
      requestVersion.current++;
    };
  }, [load]);

  const number = (value: number) => value.toLocaleString(locale);
  const value = (section: OutcomeMetric, count: number) =>
    outcome && outcomeAvailable(outcome, section) ? number(count) : "—";
  const metric = (label: string, content: React.ReactNode) => (
    <div className="rounded-md border p-3">
      <dt className="text-muted-foreground text-xs">{uiText(label)}</dt>
      <dd className="mt-1 font-semibold text-xl tabular-nums">{content}</dd>
    </div>
  );

  return (
    <Card aria-busy={loading}>
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle className="flex items-center gap-2 text-base">
            <BarChart3Icon className="size-4 text-primary" aria-hidden="true" />
            {uiText("任务结果")}
            {outcome && (
              <Badge variant="outline">{outcome.source === "archive" ? uiText("归档快照") : uiText("实时数据")}</Badge>
            )}
            {outcome?.partial && <Badge variant="secondary">{uiText("部分数据")}</Badge>}
          </CardTitle>
          <Button size="sm" variant="outline" disabled={loading} onClick={load}>
            <RefreshCwIcon className={`size-3.5 ${loading ? "animate-spin" : ""}`} aria-hidden="true" />
            {uiText("刷新结果")}
          </Button>
        </div>
        <CardDescription>{uiText("当前任务的漏洞结果、复测和资源用量。打开总览时加载，可手动刷新。")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {error && (
          <div role="alert" className="space-y-1 text-destructive text-sm">
            <p>{uiText("任务结果暂时不可用，请重试。")}</p>
            <p className="break-words text-xs">{error}</p>
            {outcome && <p>{uiText("以下保留上次成功加载的数据。")}</p>}
          </div>
        )}
        {!outcome && !error && (
          <p role="status" className="text-muted-foreground text-sm">
            {uiText("正在加载任务结果…")}
          </p>
        )}
        {outcome && (
          <>
            {outcome.partial && (
              <p role="status" className="text-amber-600 text-sm dark:text-amber-400">
                {uiText("部分记录不可用；缺失的指标以 — 显示。")}
              </p>
            )}
            <dl className="grid grid-cols-2 gap-3 lg:grid-cols-4">
              {metric("已登记漏洞", value("findings.total", outcome.findings.total))}
              {metric("已确认漏洞", value("findings.statuses", outcome.findings.confirmed_total))}
              {metric("待处理漏洞", value("findings.statuses", outcome.findings.open))}
              {metric("复测次数", value("retests", outcome.retests.total))}
            </dl>
            <div className="grid gap-4 md:grid-cols-2">
              <div className="space-y-2 rounded-md bg-muted/40 p-3">
                <h4 className="font-medium text-sm">{uiText("漏洞结果")}</h4>
                <p className="text-muted-foreground text-xs">
                  {uiText("已确认包括已确认、已解决和已修复；严重度仅统计这些漏洞。")}
                </p>
                <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
                  {(
                    [
                      ["严重", outcome.findings.critical],
                      ["高危", outcome.findings.high],
                      ["中危", outcome.findings.medium],
                      ["低危", outcome.findings.low],
                    ] as const
                  ).map(([label, count]) => (
                    <div key={label} className="flex justify-between gap-2">
                      <dt>{uiText(label)}</dt>
                      <dd className="tabular-nums">{value("findings.confirmed_severity", count)}</dd>
                    </div>
                  ))}
                </dl>
                {outcome.findings.unclassified !== undefined && (
                  <p className="text-muted-foreground text-xs">
                    {uiText("未分级")}: {value("findings.confirmed_severity", outcome.findings.unclassified)}
                  </p>
                )}
                <details className="pt-1 text-xs">
                  <summary className="cursor-pointer">{uiText("查看状态分布")}</summary>
                  <dl className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1">
                    {(
                      [
                        ["待确认", outcome.findings.pending],
                        ["处理中", outcome.findings.in_progress],
                        ["已确认", outcome.findings.confirmed],
                        ["已解决", outcome.findings.resolved],
                        ["已修复", outcome.findings.fixed],
                        ["误报", outcome.findings.false_positive],
                        ["已忽略", outcome.findings.ignored],
                        ["重复", outcome.findings.duplicate],
                        ["风险已接受", outcome.findings.risk_accepted],
                      ] as const
                    ).map(([label, count]) => (
                      <div key={label} className="flex justify-between gap-2">
                        <dt>{uiText(label)}</dt>
                        <dd className="tabular-nums">{value("findings.statuses", count)}</dd>
                      </div>
                    ))}
                  </dl>
                </details>
              </div>
              <div className="space-y-2 rounded-md bg-muted/40 p-3">
                <h4 className="font-medium text-sm">{uiText("复测结果")}</h4>
                <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
                  {(
                    [
                      ["待执行", outcome.retests.pending],
                      ["执行中", outcome.retests.running],
                      ["已完成", outcome.retests.completed],
                      ["已停止", outcome.retests.stopped],
                      ["已复现", outcome.retests.reproduced],
                      ["已修复", outcome.retests.fixed],
                      ["结论不明确", outcome.retests.inconclusive],
                      ["执行失败", outcome.retests.failed],
                    ] as const
                  ).map(([label, count]) => (
                    <div key={label} className="flex justify-between gap-2">
                      <dt>{uiText(label)}</dt>
                      <dd className="tabular-nums">{value("retests", count)}</dd>
                    </div>
                  ))}
                </dl>
                <p className="text-muted-foreground text-xs">
                  {uiText("每次复测单独计数；复现、修复和结论不明确仅统计已完成的复测。")}
                </p>
              </div>
            </div>
            <dl className="grid grid-cols-2 gap-3 lg:grid-cols-4">
              {metric("已记录模型调用", value("tokens", outcome.tokens.calls))}
              {metric("工具调用", value("tool_calls", outcome.tool_calls))}
              {metric("技能缺失", value("skill_misses", outcome.skill_misses))}
              {metric(
                "自然经过时间",
                outcomeAvailable(outcome, "wall_seconds") ? outcomeDuration(outcome.wall_seconds) : "—",
              )}
            </dl>
            <div className="space-y-1 text-muted-foreground text-xs">
              <p>
                {uiText("输入 Token")}: {value("tokens", outcome.tokens.input_tokens)} · {uiText("输出 Token")}:{" "}
                {value("tokens", outcome.tokens.output_tokens)} · {uiText("缓存读取")}:{" "}
                {value("tokens", outcome.tokens.cache_read_tokens)} · {uiText("缓存写入")}:{" "}
                {value("tokens", outcome.tokens.cache_write_tokens)}
              </p>
              <p>
                {uiText(
                  "Token 表示已记录用量，费用未计算。包含已记录的失败调用；内部 HTTP 和空响应重试无法单独区分，复测独立会话不计入。",
                )}
              </p>
              <p>
                {uiText(
                  "自然经过时间从首次运行至完成或当前时刻计算，包含暂停和等待，格式为 时:分:秒 或 分:秒；未开始或完成时间未知时显示 —。",
                )}
              </p>
              <p>{uiText("漏洞仅统计当前任务所属记录，包含重复条目；继承漏洞和跨任务去重结果不计入。")}</p>
            </div>
            <details className="rounded-md border p-3 text-xs">
              <summary className="cursor-pointer font-medium">{uiText("统计范围与可用性")}</summary>
              <dl className="mt-3 space-y-2">
                {(Object.entries(outcome.scope) as [keyof TaskOutcome["scope"], string][]).map(([section, scope]) => (
                  <div key={section} className="grid gap-1 sm:grid-cols-[7rem_1fr]">
                    <dt className="font-medium">
                      {uiText(
                        {
                          findings: "漏洞结果",
                          tokens: "Token 用量",
                          retests: "复测结果",
                          tools: "工具调用",
                          skills: "技能缺失",
                          time: "自然经过时间",
                          cost: "费用",
                        }[section],
                      )}
                    </dt>
                    <dd className="break-words text-muted-foreground">{scope}</dd>
                  </div>
                ))}
              </dl>
              {outcome.unavailable.length > 0 && (
                <p className="mt-3 break-words text-muted-foreground">
                  {uiText("不可用指标")}: {outcome.unavailable.join(", ")}
                </p>
              )}
            </details>
          </>
        )}
      </CardContent>
    </Card>
  );
}
