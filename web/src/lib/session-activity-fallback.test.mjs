import ts from "typescript";

import { readActivityHistoryGap, startActivityFallback } from "./session-activity-fallback.ts";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

const activity = (seq, worker = "mainagent") => ({ seq, worker, kind: "text", ts: String(seq), summary: String(seq) });
const page = (seqs, hasMore = true) => ({
  items: seqs.map((seq) => activity(seq)),
  earliestCursor: Math.min(...seqs),
  hasMore,
});
function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}
async function settle() {
  for (let i = 0; i < 20; i++) await Promise.resolve();
}

test("a gap larger than the latest page reads every page back to the known anchor", async () => {
  const requests = [];
  const pages = new Map([
    [0, page([701, 702, 703])],
    [701, page([501, 502, 503])],
    [501, page([301, 302, 303])],
    [301, page([99, 100, 101])],
  ]);
  const rows = await readActivityHistoryGap(
    async (before) => {
      requests.push(before);
      return pages.get(before);
    },
    100,
    () => true,
  );
  assert.deepEqual(requests, [0, 701, 501, 301]);
  assert.deepEqual(
    rows.map((row) => row.seq),
    [101, 301, 302, 303, 501, 502, 503, 701, 702, 703],
  );
});

test("empty history starts at zero and overlapping pages deduplicate by sequence", async () => {
  const pages = new Map([
    [0, page([3, 4])],
    [3, page([1, 2, 3], false)],
  ]);
  const rows = await readActivityHistoryGap(
    async (before) => pages.get(before),
    0,
    () => true,
  );
  assert.deepEqual(
    rows.map((row) => row.seq),
    [1, 2, 3, 4],
  );
});

test("cancellation during an older page discards the entire partial gap", async () => {
  let current = true;
  const older = deferred();
  const requests = [];
  const result = readActivityHistoryGap(
    (before) => {
      requests.push(before);
      return before === 0 ? Promise.resolve(page([5, 6])) : older.promise;
    },
    1,
    () => current,
  );
  await settle();
  current = false;
  older.resolve(page([3, 4]));
  assert.equal(await result, undefined);
  assert.deepEqual(requests, [0, 5]);
});

test("a failed or nonadvancing older page never publishes a partial gap", async () => {
  await assert.rejects(
    readActivityHistoryGap(
      async (before) => {
        if (before === 0) return page([5, 6]);
        throw new Error("offline");
      },
      1,
      () => true,
    ),
    /offline/,
  );
  await assert.rejects(
    readActivityHistoryGap(
      async () => page([5, 6]),
      1,
      () => true,
    ),
    /did not advance/,
  );
});

// Execute the production fallback scope and effect so navigation/recovery tests
// cover their actual dependency and stale-response guards, not a copied model.
const source = readFileSync(
  new URL("../app/(main)/function/tasks/detail/_tabs/sessions-tab.tsx", import.meta.url),
  "utf8",
);
const ast = ts.createSourceFile("sessions-tab.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const component = ast.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "SessionsTab");
function hasIdentifier(node, name) {
  if (ts.isIdentifier(node) && node.text === name) return true;
  return ts.forEachChild(node, (child) => hasIdentifier(child, name)) ?? false;
}
const selected = component.body.statements.filter((node) => {
  if (ts.isVariableStatement(node)) {
    return node.declarationList.declarations.some(
      (declaration) =>
        ts.isIdentifier(declaration.name) &&
        ["fallbackEnabled", "fallbackScopeRef", "fallbackScope"].includes(declaration.name.text),
    );
  }
  if (ts.isIfStatement(node)) return hasIdentifier(node, "fallbackScopeRef");
  return ts.isExpressionStatement(node) && hasIdentifier(node, "startActivityFallback");
});
assert.equal(selected.length, 5, "production fallback scope and effect must exist");
const compiled = ts.transpileModule(
  `function render({ taskId, activeKey, sseLive, activeState, active }) {
  ${selected.map((node) => node.getText(ast)).join("\n")}
}
module.exports = render;`,
  { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } },
).outputText;

function fallbackHarness(t, options = {}) {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const requests = [];
  const hooks = [];
  const effects = new Map();
  const storeRef = { current: { "main:0": { loaded: true, items: [activity(100)], earliestSeq: 100, hasMore: true } } };
  let values = { taskId: "A", activeKey: "main:0", sseLive: false, active: {}, ...options };
  let nextHook = 0;
  let updates = 0;
  const module = { exports: {} };
  const sameDeps = (a, b) => a?.length === b.length && a.every((value, index) => Object.is(value, b[index]));
  const api = {
    activityHistory(taskId, session, before, limit) {
      const request = { taskId, session, before, limit, ...deferred() };
      requests.push(request);
      return request.promise;
    },
    activity(taskId, query) {
      const request = { taskId, ...query, ...deferred() };
      requests.push(request);
      return request.promise;
    },
  };
  runInNewContext(compiled, {
    module,
    MOCK: false,
    readActivityHistoryGap,
    startActivityFallback,
    api,
    storeRef,
    PAGE: 200,
    SYSTEM_SCAN_PAGE: 500,
    MAX_KEEP: 4000,
    atBottomRef: { current: true },
    sessionKeyOf: (item) =>
      item.worker === "system" || item.kind === "llm_switch" || item.kind === "llm_failover" ? "system" : "other",
    mergeBySeq: (current, incoming) =>
      [...new Map([...current, ...incoming].map((item) => [item.seq, item])).values()].sort((a, b) => a.seq - b.seq),
    patchStore(key, update) {
      const current = storeRef.current[key];
      const next = update(current);
      if (next !== current) {
        storeRef.current = { ...storeRef.current, [key]: next };
        updates++;
      }
    },
    React: {
      useRef(initial) {
        const index = nextHook++;
        hooks[index] ??= { current: initial };
        return hooks[index];
      },
      useEffect(callback, deps) {
        const index = nextHook++;
        if (!sameDeps(hooks[index]?.deps, deps)) {
          effects.set(index, () => {
            hooks[index]?.cleanup?.();
            hooks[index] = { deps, cleanup: callback() };
          });
        }
      },
    },
  });
  const render = (next = {}, commit = true) => {
    values = { ...values, ...next };
    nextHook = 0;
    module.exports({ ...values, activeState: storeRef.current[values.activeKey] });
    if (commit) {
      for (const effect of effects.values()) effect();
      effects.clear();
    }
  };
  render();
  return {
    requests,
    storeRef,
    render,
    updates: () => updates,
    async tick() {
      t.mock.timers.tick(2000);
      await settle();
    },
    async resolve(index, result) {
      requests[index].resolve(result);
      await settle();
    },
    async reject(index) {
      requests[index].reject(new Error("offline"));
      await settle();
    },
    stop() {
      for (const hook of hooks) hook?.cleanup?.();
    },
  };
}

test("polls serialize, retry a failed gap from its old anchor, and avoid duplicate updates", async (t) => {
  const view = fallbackHarness(t);
  await view.tick();
  await view.tick();
  assert.equal(view.requests.length, 1, "no overlapping tick while latest request is pending");
  await view.resolve(0, page([301, 302]));
  assert.equal(view.requests[1].before, 301);
  assert.equal(view.updates(), 0, "do not publish the latest page before recovering the gap");
  await view.reject(1);
  await view.tick();
  assert.equal(view.requests[2].before, 0);
  await view.resolve(2, page([101, 102], false));
  assert.equal(view.updates(), 1);
  await view.tick();
  await view.resolve(3, page([101, 102], false));
  assert.equal(view.updates(), 1);
  view.stop();
});

test("the production poll recovers a 501-row outage across all 200-row history pages", async (t) => {
  const view = fallbackHarness(t);
  const seqs = (from, count) => Array.from({ length: count }, (_, index) => from + index);
  await view.tick();
  assert.equal(view.requests[0].limit, 200);
  await view.resolve(0, page(seqs(402, 200)));
  assert.equal(view.requests[1].before, 402);
  await view.resolve(1, page(seqs(202, 200)));
  assert.equal(view.requests[2].before, 202);
  await view.resolve(2, page(seqs(2, 200), false));
  assert.deepEqual(
    Array.from(view.storeRef.current["main:0"].items, (row) => row.seq),
    seqs(100, 502),
  );
  assert.equal(view.updates(), 1);
  view.stop();
});

test("returning to the same task and session cannot revive an old scope", async (t) => {
  const view = fallbackHarness(t);
  await view.tick();
  view.render({ taskId: "B" });
  view.render({ taskId: "A" });
  await view.resolve(0, page([101, 102], false));
  assert.equal(view.updates(), 0);
  await view.tick();
  assert.equal(view.requests.length, 2);
  await view.resolve(1, page([101, 102], false));
  assert.equal(view.updates(), 1);
  view.stop();
});

test("session/task switches discard pending rows before effect cleanup and never revive A", async (t) => {
  const view = fallbackHarness(t);
  await view.tick();
  view.storeRef.current["intent:7"] = { loaded: true, items: [activity(200)], earliestSeq: 200 };
  view.render({ activeKey: "intent:7" }, false);
  await view.resolve(0, page([101, 102], false));
  assert.equal(view.updates(), 0);
  view.render();
  await view.tick();
  assert.equal(view.requests[1].session, "intent:7");
  view.render({ taskId: "B" });
  view.render({ taskId: "A", activeKey: "main:0" });
  await view.resolve(1, page([201, 202], false));
  assert.equal(view.updates(), 0);
  await view.tick();
  assert.equal(view.requests[2].taskId, "A");
  assert.equal(view.requests[2].session, "main:0");
  view.stop();
});

test("SSE recovery and unmount discard pending pages and stop retry timers", async (t) => {
  const view = fallbackHarness(t);
  await view.tick();
  await view.resolve(0, page([301, 302]));
  view.render({ sseLive: true }, false);
  await view.resolve(1, page([101, 102], false));
  assert.equal(view.updates(), 0);
  view.render();
  await view.tick();
  assert.equal(view.requests.length, 2);
  view.render({ sseLive: false });
  await view.tick();
  view.stop();
  await view.resolve(2, page([101, 102], false));
  await view.tick();
  assert.equal(view.updates(), 0);
  assert.equal(view.requests.length, 3);
});

test("an SSE row arriving during a poll is deduplicated and retained", async (t) => {
  const view = fallbackHarness(t);
  await view.tick();
  view.storeRef.current["main:0"].items.push(activity(102));
  await view.resolve(0, page([100, 101, 102], false));
  assert.deepEqual(
    Array.from(view.storeRef.current["main:0"].items, (row) => row.seq),
    [100, 101, 102],
  );
  assert.equal(view.updates(), 1);
  view.stop();
});

test("only loaded local transcripts poll, and active system scanning advances over unrelated rows", async (t) => {
  const view = fallbackHarness(t, { active: { inherited: true } });
  await view.tick();
  assert.equal(view.requests.length, 0);
  view.render({ active: {}, activeKey: "plan" });
  await view.tick();
  assert.equal(view.requests.length, 0, "first history load owns an unloaded session");
  view.storeRef.current.system = { loaded: true, items: [activity(50, "system")], earliestSeq: 50 };
  view.render({ activeKey: "system" });
  await view.tick();
  assert.equal(view.requests[0].since, 50);
  await view.resolve(0, { items: [activity(51), activity(52, "system")], cursor: 52 });
  assert.deepEqual(
    Array.from(view.storeRef.current.system.items, (row) => row.seq),
    [50, 52],
  );
  await view.tick();
  assert.equal(view.requests[1].since, 52);
  await view.resolve(1, { items: [activity(53)], cursor: 53 });
  assert.equal(view.updates(), 1);
  await view.tick();
  assert.equal(view.requests[2].since, 53);
  view.stop();
});
