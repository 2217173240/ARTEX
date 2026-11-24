import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";

const source = readFileSync(new URL("./api.ts", import.meta.url), "utf8");
const tree = ts.createSourceFile("api.ts", source, ts.ScriptTarget.Latest, true);
const selected = tree.statements.filter((node) => ["ApiError", "conversationTailRetryDelay"].includes(node.name?.text));
assert.equal(selected.length, 2);
const module = { exports: {} };
runInNewContext(
  ts.transpileModule(selected.map((node) => node.getText(tree)).join("\n"), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
  }).outputText,
  { module, exports: module.exports, Error },
);
const { ApiError, conversationTailRetryDelay } = module.exports;

test("conversation tail retries transient errors within a bounded delay budget", () => {
  for (const error of [
    new TypeError("network unavailable"),
    new ApiError(408, "timeout"),
    new ApiError(429, "retry later"),
    new ApiError(503, "unavailable"),
  ]) {
    assert.deepEqual(
      [1, 2, 3, 4].map((failures) => conversationTailRetryDelay(error, failures)),
      [1000, 2000, 4000, null],
    );
  }
});

test("conversation tail pauses permanent client errors for explicit retry", () => {
  for (const status of [400, 401, 403, 404, 422]) {
    assert.equal(conversationTailRetryDelay(new ApiError(status, "request rejected"), 1), null);
  }
});
