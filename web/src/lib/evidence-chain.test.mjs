import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

const module = { exports: {} };
runInNewContext(
  ts.transpileModule(readFileSync(new URL("./evidence-chain.ts", import.meta.url), "utf8"), {
    compilerOptions: { module: ts.ModuleKind.CommonJS },
  }).outputText,
  { module, exports: module.exports },
);
const { evidencePayload, evidenceObservationIds } = module.exports;

test("evidence payload accepts structured objects while keeping legacy or malformed content out of typed fields", () => {
  assert.equal(evidencePayload({ payload: "legacy evidence text" }), null);
  assert.equal(evidencePayload({ payload: "null" }), null);
  assert.equal(evidencePayload({ payload: "[]" }), null);
  assert.equal(
    evidencePayload({ payload: '{"summary":"raw evidence","source_snapshot":{"request":"GET /"}}' }).source_snapshot
      .request,
    "GET /",
  );
});

test("observation references retain identifiers and ignore malformed values", () => {
  assert.deepEqual(Array.from(evidenceObservationIds({ observation_ids: [12, "13", 12, null, { id: 14 }] })), [
    "12",
    "13",
  ]);
  assert.deepEqual(Array.from(evidenceObservationIds({ observation_ids: "12" })), []);
});
