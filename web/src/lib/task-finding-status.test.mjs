import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

// Execute the production callback in isolation; task rows and finding DTOs have
// independent identifiers even when their numeric values happen to collide.
function statusHarness(initialRows) {
  let rows = initialRows;
  const source = readFileSync(
    new URL("../app/(main)/function/tasks/detail/_tabs/findings-tab.tsx", import.meta.url),
    "utf8",
  );
  const file = ts.createSourceFile("findings-tab.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  let callback;
  const visit = (node) => {
    if (ts.isVariableDeclaration(node) && node.name.getText(file) === "onStatus")
      callback = node.initializer.arguments[0].getText(file);
    ts.forEachChild(node, visit);
  };
  visit(file);
  assert.ok(callback, "missing real task status callback");
  const requests = [];
  const module = { exports: {} };
  const body = ts.transpileModule(`module.exports = ${callback};`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText;
  runInNewContext(body, {
    module,
    requestVersion: { current: 0 },
    setFindings: (update) => {
      rows = update(rows);
    },
    taskId: "task-context",
    uiText: (value) => value,
    statusMeta: (_domain, value) => ({ label: value }),
    toast: {
      success() {
        /* Toast presentation is covered elsewhere. */
      },
      error() {
        /* Preserve callback error handling. */
      },
    },
    api: {
      setFindingStatus(id, status, context) {
        let resolve, reject;
        const promise = new Promise((yes, no) => {
          resolve = yes;
          reject = no;
        });
        requests.push({ id, status, context, resolve, reject });
        return promise;
      },
    },
  });
  return { rows: () => rows, requests, change: (row, status) => module.exports(row, status) };
}

const taskRow = (nodeId, findingId) => ({
  id: nodeId,
  finding_id: findingId,
  task_id: "task-context",
  task_description: "Task row context",
  intent_id: `intent-${nodeId}`,
  param_id: `param-${nodeId}`,
  source_task_id: "source-task",
  inherited: false,
  ts: "2026-10-10T00:00:00Z",
  summary: "Node evidence summary",
  evidence: "Node evidence",
  name: "Old name",
  vulnclass: "old-class",
  severity: "medium",
  status: "pending",
});
const updated = (id, status) => ({
  id,
  finding_id: id,
  task_id: "owner-task",
  task_description: "Finding owner",
  intent_id: "owner-intent",
  name: "Updated name",
  vulnclass: "new-class",
  severity: "high",
  status,
  reviewed_by: "ARTEX (shared admin)",
  reviewed_at: "2026-10-10T01:00:00Z",
  reviewed_status: status,
  ts: "2026-10-10T01:00:00Z",
  summary: "Finding summary",
  evidence: "Finding evidence",
});

test("task status DTO merge preserves node identity and provenance despite a colliding finding ID", async () => {
  const first = taskRow("7", "101"),
    second = taskRow("101", "202");
  const h = statusHarness([first, second]);
  const pending = h.change(first, "confirmed");
  assert.equal(h.requests[0].id, "101");
  assert.equal(h.requests[0].context, "task-context");
  h.requests[0].resolve(updated("101", "confirmed"));
  await pending;
  const row = h.rows()[0];
  for (const key of [
    "id",
    "finding_id",
    "task_id",
    "task_description",
    "intent_id",
    "param_id",
    "source_task_id",
    "inherited",
    "ts",
    "summary",
    "evidence",
  ])
    assert.equal(row[key], first[key], key);
  assert.equal(row.status, "confirmed");
  assert.equal(row.name, "Updated name");
  assert.equal(row.severity, "high");
  assert.equal(row.reviewed_status, "confirmed");
  assert.equal(row.reviewed_at, "2026-10-10T01:00:00Z");
  assert.equal(h.rows()[1], second);

  // Updating node 101 must not match the first row, whose standalone finding ID is also 101.
  const next = h.change(h.rows()[1], "ignored");
  assert.equal(h.requests[1].id, "202");
  assert.equal(h.rows()[0].status, "confirmed");
  h.requests[1].resolve(updated("202", "ignored"));
  await next;
  assert.equal(h.rows()[0].status, "confirmed");
  assert.equal(h.rows()[0].reviewed_status, "confirmed");
  assert.equal(h.rows()[1].id, "101");
  assert.equal(h.rows()[1].status, "ignored");
});
