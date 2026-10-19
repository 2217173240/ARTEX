import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

const source = readFileSync(new URL("./task-outcome.ts", import.meta.url), "utf8");
const module = { exports: {} };
runInNewContext(ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText, {
  module,
  exports: module.exports,
});
const { outcomeAvailable, outcomeDuration } = module.exports;

test("legacy archive preserves known finding totals while keeping unavailable dimensions unknown", () => {
  const outcome = {
    unavailable: [
      "findings.statuses",
      "findings.confirmed_severity",
      "tokens",
      "retests",
      "tool_calls",
      "skill_misses",
      "wall_seconds",
    ],
  };
  assert.equal(outcomeAvailable(outcome, "findings.total"), true);
  for (const metric of outcome.unavailable) assert.equal(outcomeAvailable(outcome, metric), false, metric);
  assert.equal(outcomeAvailable({ unavailable: [...outcome.unavailable, "findings.total"] }, "findings.total"), false);
});

test("live zero measurements remain available and duration preserves unknown values", () => {
  assert.equal(outcomeAvailable({ unavailable: [] }, "tokens"), true);
  assert.equal(outcomeDuration(null), "—");
  assert.equal(outcomeDuration(Number.NaN), "—");
  assert.equal(outcomeDuration(-1), "—");
  assert.equal(outcomeDuration(0), "0:00");
  assert.equal(outcomeDuration(65), "1:05");
  assert.equal(outcomeDuration(3601), "1:00:01");
});
