import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

const source = readFileSync(new URL("./finding-navigation.ts", import.meta.url), "utf8");
const module = { exports: {} };
runInNewContext(ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText, {
  module,
  exports: module.exports,
  URLSearchParams,
});
const { findingReportHref, findingReturnHref, returnTaskId, taskTab, taskTabHref } = module.exports;

test("task return accepts positive int64 registry IDs and rejects arbitrary destinations", () => {
  for (const value of ["1", "42", "9223372036854775807"]) {
    assert.equal(returnTaskId(value), value);
    const href = new URL(findingReturnHref(value), "https://artex.example");
    assert.equal(href.pathname, "/function/tasks/detail");
    assert.equal(href.searchParams.get("id"), value);
    assert.equal(href.searchParams.get("tab"), "findings");
  }
  for (const value of [
    null,
    undefined,
    "",
    "0",
    "-1",
    "01",
    "1.5",
    "9223372036854775808",
    "9".repeat(10000),
    "1&tab=report",
    "javascript:alert(1)",
    "//evil.example",
    "https://evil.example",
    "%31",
    "t-acme-web",
  ]) {
    assert.equal(returnTaskId(value), undefined);
    assert.equal(findingReturnHref(value), "/function/findings");
  }
});

test("original and unified reports carry navigation origin independently of inherited access context", () => {
  for (const kind of ["detail", "case"]) {
    const inherited = new URL(findingReportHref(kind, "7&extra=1", "12&mode=write", "42"), "https://artex.example");
    assert.equal(inherited.pathname, `/function/findings/${kind}`);
    assert.equal(inherited.searchParams.get("id"), "7&extra=1");
    assert.equal(inherited.searchParams.get("context_task"), "12&mode=write");
    assert.equal(inherited.searchParams.get("return_task"), "42");
    assert.equal(inherited.searchParams.get("return_tab"), "findings");
    assert.equal(inherited.searchParams.size, 4);
    const own = new URL(findingReportHref(kind, "7", undefined, "42"), "https://artex.example");
    assert.equal(own.searchParams.has("context_task"), false);
    assert.equal(own.searchParams.get("return_task"), "42");
    const global = new URL(findingReportHref(kind, "7"), "https://artex.example");
    assert.equal(global.searchParams.has("return_task"), false);
    assert.equal(global.searchParams.has("return_tab"), false);
    assert.equal(
      new URL(findingReportHref(kind, "7", "12", "//evil"), "https://artex.example").searchParams.has("return_task"),
      false,
    );
  }
});

test("URL tabs validate each history entry and retain unrelated query state", () => {
  for (const tab of [
    "sessions",
    "overview",
    "graph",
    "evidence",
    "broadcast",
    "findings",
    "retests",
    "assets",
    "coverage",
    "intercept",
    "report",
  ]) {
    const href = new URL(
      taskTabHref("id=42&session=main%3A2&focus=approval&tag=a&tag=b", tab),
      "https://artex.example",
    );
    assert.equal(taskTab(href.searchParams.get("tab")), tab);
    assert.equal(href.searchParams.get("id"), "42");
    assert.equal(href.searchParams.get("session"), "main:2");
    assert.equal(href.searchParams.get("focus"), "approval");
    assert.deepEqual(href.searchParams.getAll("tag"), ["a", "b"]);
  }
  for (const value of [null, "", "constructor", "Findings", "unknown"]) assert.equal(taskTab(value), "sessions");
  assert.equal(taskTab(new URLSearchParams("id=43&tab=report").get("tab")), "report");
  assert.equal(taskTab(new URLSearchParams("id=42&tab=findings").get("tab")), "findings");
});

test("paired suggestion reports retain the opening task across detail and unified report navigation", () => {
  for (const contextTask of [undefined, "12"]) {
    for (const returnTab of ["findings", "retests"]) {
      for (const reportId of ["7", "8"]) {
        const detail = new URL(
          findingReportHref("detail", reportId, contextTask, "42", returnTab),
          "https://artex.example",
        );
        const unified = new URL(
          findingReportHref(
            "case",
            "9",
            detail.searchParams.get("context_task") ?? undefined,
            detail.searchParams.get("return_task") ?? undefined,
            detail.searchParams.get("return_tab") ?? undefined,
          ),
          "https://artex.example",
        );
        assert.equal(detail.searchParams.get("id"), reportId);
        assert.equal(unified.searchParams.get("context_task"), contextTask ?? null);
        assert.equal(
          findingReturnHref(unified.searchParams.get("return_task"), unified.searchParams.get("return_tab")),
          `/function/tasks/detail?id=42&tab=${returnTab}`,
        );
      }
    }
  }
});

test("return tabs accept only findings and retests within a valid task origin", () => {
  for (const tab of [
    undefined,
    null,
    "",
    "findings",
    "report",
    "Retests",
    "constructor",
    "retests&tab=report",
    "//evil",
  ]) {
    assert.equal(findingReturnHref("42", tab), "/function/tasks/detail?id=42&tab=findings");
    for (const kind of ["detail", "case"]) {
      const report = new URL(findingReportHref(kind, "7", "12", "42", tab), "https://artex.example");
      assert.equal(report.searchParams.get("return_tab"), "findings");
      assert.equal(report.searchParams.get("context_task"), "12");
    }
  }
  assert.equal(findingReturnHref("42", "retests"), "/function/tasks/detail?id=42&tab=retests");
  for (const task of [undefined, "", "0", "-1", "01", "9223372036854775808", "//evil"]) {
    for (const tab of ["findings", "retests", "report"]) {
      assert.equal(findingReturnHref(task, tab), "/function/findings");
      const report = new URL(findingReportHref("detail", "7", "12", task, tab), "https://artex.example");
      assert.equal(report.searchParams.has("return_task"), false);
      assert.equal(report.searchParams.has("return_tab"), false);
      assert.equal(report.searchParams.get("context_task"), "12");
    }
  }
});
