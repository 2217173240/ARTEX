import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { runInNewContext } from "node:vm";

const libDir = path.dirname(fileURLToPath(import.meta.url));

function loadTS(filename, context = {}, cache = new Map()) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = { exports: {} };
  cache.set(filename, module);
  const output = ts.transpileModule(readFileSync(filename, "utf8"), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
  }).outputText;
  runInNewContext(output, {
    module,
    exports: module.exports,
    URLSearchParams,
    structuredClone,
    require: (name) => loadTS(path.resolve(path.dirname(filename), `${name}.ts`), context, cache),
    ...context,
  });
  return module.exports;
}

const approvalHistory = loadTS(path.join(libDir, "approval-history.ts"));
const approvalQueue = loadTS(path.join(libDir, "approval-queue.ts"));
const { approvalRemainingSeconds, isRowDeciding, addDecidingId, removeDecidingId } = approvalQueue;

test("approvalRemainingSeconds computes active, approaching, expired, disabled, and invalid timestamps", () => {
  const createdAt = "2026-11-27T10:00:00.000Z";
  const createdMs = Date.parse(createdAt);

  // Active countdown
  assert.equal(
    approvalRemainingSeconds({ created_at: createdAt, timeout_enabled: true, timeout_seconds: 60 }, createdMs + 15_000),
    45,
  );
  // Approaching expiry rounds up partial remaining second
  assert.equal(
    approvalRemainingSeconds({ created_at: createdAt, timeout_enabled: true, timeout_seconds: 60 }, createdMs + 59_250),
    1,
  );
  // Expired clamps to 0
  assert.equal(
    approvalRemainingSeconds({ created_at: createdAt, timeout_enabled: true, timeout_seconds: 60 }, createdMs + 60_000),
    0,
  );
  assert.equal(
    approvalRemainingSeconds({ created_at: createdAt, timeout_enabled: true, timeout_seconds: 60 }, createdMs + 95_000),
    0,
  );
  // Disabled or invalid timeout config returns null
  assert.equal(
    approvalRemainingSeconds({ created_at: createdAt, timeout_enabled: false, timeout_seconds: 60 }, createdMs + 1_000),
    null,
  );
  assert.equal(
    approvalRemainingSeconds({ created_at: createdAt, timeout_enabled: true, timeout_seconds: 0 }, createdMs + 1_000),
    null,
  );
  assert.equal(
    approvalRemainingSeconds({ created_at: createdAt, timeout_enabled: true, timeout_seconds: -5 }, createdMs + 1_000),
    null,
  );
  // Invalid or missing created_at returns null
  assert.equal(
    approvalRemainingSeconds({ created_at: "not-a-date", timeout_enabled: true, timeout_seconds: 60 }, createdMs),
    null,
  );
  assert.equal(approvalRemainingSeconds({ timeout_enabled: true, timeout_seconds: 60 }, createdMs), null);
});

test("per-row deciding lock helpers track individual row IDs immutably", () => {
  const initial = new Set();
  assert.equal(isRowDeciding(initial, 1), false);

  const withOne = addDecidingId(initial, 1);
  assert.equal(isRowDeciding(initial, 1), false);
  assert.equal(isRowDeciding(withOne, 1), true);
  assert.equal(isRowDeciding(withOne, 2), false);

  // Adding an existing ID returns the same Set reference
  assert.equal(addDecidingId(withOne, 1), withOne);

  const withTwo = addDecidingId(withOne, 2);
  assert.equal(isRowDeciding(withTwo, 1), true);
  assert.equal(isRowDeciding(withTwo, 2), true);

  const withoutOne = removeDecidingId(withTwo, 1);
  assert.equal(isRowDeciding(withoutOne, 1), false);
  assert.equal(isRowDeciding(withoutOne, 2), true);

  // Removing an absent ID returns the same Set reference
  assert.equal(removeDecidingId(withoutOne, 99), withoutOne);
});

function elements(node) {
  if (Array.isArray(node)) return node.flatMap(elements);
  if (node && typeof node === "object") return [node, ...elements(node.props?.children)];
  return [];
}

function text(node) {
  if (Array.isArray(node)) return node.map(text).join("");
  if (node && typeof node === "object") return text(node.props?.children);
  return node == null || node === false ? "" : String(node);
}

const sameDeps = (a, b) => a && b && a.length === b.length && a.every((item, index) => Object.is(item, b[index]));
const translateUI = (value, params = {}) => value.replace(/\{(\w+)\}/g, (match, key) => String(params[key] ?? match));

function queueHarness(options = {}) {
  const hooks = [];
  const effects = [];
  const timers = new Map();
  const visibilityListeners = new Set();
  const decideCalls = [];
  const loadCalls = { history: 0, pending: 0 };
  const toastMessages = { success: [], error: [] };
  const doc = {
    hidden: Boolean(options.initialHidden),
    addEventListener(event, fn) {
      if (event === "visibilitychange") visibilityListeners.add(fn);
    },
    removeEventListener(event, fn) {
      if (event === "visibilitychange") visibilityListeners.delete(fn);
    },
  };
  let hookIndex = 0;
  let timerId = 0;
  let dirty = true;
  let tree;

  const React = {
    Fragment: Symbol.for("react.fragment"),
    useState(initial) {
      const i = hookIndex++;
      if (!hooks[i]) {
        hooks[i] = { value: typeof initial === "function" ? initial() : initial };
      }
      return [
        hooks[i].value,
        (update) => {
          const next = typeof update === "function" ? update(hooks[i].value) : update;
          if (!Object.is(next, hooks[i].value)) {
            hooks[i].value = next;
            dirty = true;
          }
        },
      ];
    },
    useRef(value) {
      const i = hookIndex++;
      hooks[i] ??= { current: value };
      return hooks[i];
    },
    useId() {
      hookIndex++;
      return "approval-queue";
    },
    useCallback(callback, deps) {
      const i = hookIndex++;
      if (!sameDeps(hooks[i]?.deps, deps)) hooks[i] = { deps, callback };
      return hooks[i].callback;
    },
    useEffect(callback, deps) {
      const i = hookIndex++;
      const before = hooks[i];
      if (!sameDeps(before?.deps, deps)) {
        hooks[i] = { deps, cleanup: before?.cleanup };
        effects.push(() => {
          hooks[i].cleanup?.();
          hooks[i].cleanup = callback();
        });
      }
    },
    useSyncExternalStore(_sub, _getSnapshot, getServerSnapshot) {
      hookIndex++;
      return getServerSnapshot();
    },
  };

  function deferred(extra = {}) {
    let resolve;
    let reject;
    const promise = new Promise((res, rej) => {
      resolve = res;
      reject = rej;
    });
    return { ...extra, promise, resolve, reject };
  }

  let pendingQueue = options.initialPending ?? [];
  let historyQueue = options.initialHistory ?? [];

  const api = {
    interceptHistoryPage: () => {
      loadCalls.history += 1;
      return Promise.resolve({ items: historyQueue, total: historyQueue.length });
    },
    interceptTaskPage: () => {
      loadCalls.history += 1;
      return Promise.resolve({ items: historyQueue, total: historyQueue.length });
    },
    interceptPending: () => {
      loadCalls.pending += 1;
      return Promise.resolve(pendingQueue);
    },
    interceptDecide: (id, decision) => {
      const d = deferred({ id, decision });
      decideCalls.push(d);
      return d.promise;
    },
  };

  const schedule = (callback, delay, interval = false) => {
    const id = ++timerId;
    timers.set(id, { callback, delay, interval });
    return id;
  };
  const components = new Proxy({}, { get: (_, name) => name });
  const jsxRuntime = {
    jsx: (type, props) => ({ type, props }),
    jsxs: (type, props) => ({ type, props }),
    Fragment: React.Fragment,
  };

  const module = loadTS(path.join(libDir, "../components/approval-records.tsx"), {
    require: (name) =>
      ({
        react: React,
        "react/jsx-runtime": jsxRuntime,
        "@/lib/api": { api },
        "@/lib/approval-history": approvalHistory,
        "@/lib/approval-queue": approvalQueue,
        "@/lib/i18n": { useI18n: () => ({ t: translateUI }) },
        "@/lib/utils": { cn: (...values) => values.filter(Boolean).join(" ") },
        sonner: {
          toast: {
            success: (msg) => toastMessages.success.push(msg),
            error: (msg) => toastMessages.error.push(msg),
          },
        },
      })[name] ?? components,
    document: doc,
    setTimeout: schedule,
    clearTimeout: (id) => timers.delete(id),
    setInterval: (callback, delay) => schedule(callback, delay, true),
    clearInterval: (id) => timers.delete(id),
  });

  async function flush() {
    for (let turn = 0; turn < 30; turn++) {
      if (dirty) {
        dirty = false;
        hookIndex = 0;
        tree = module.ApprovalRecords({ readOnly: Boolean(options.readOnly) });
        effects.splice(0).forEach((effect) => {
          effect();
        });
      }
      await Promise.resolve();
    }
    assert.equal(dirty, false);
  }

  function renderPendingTable() {
    const tableEl = elements(tree).find(
      (item) =>
        typeof item.type === "function" && item.type.name === "ApprovalTable" && item.props.label === "待处理审批",
    );
    if (!tableEl) return null;
    // Render ApprovalTable cleanly using a separate tiny hook store so we can inspect row buttons & badges
    const tableHooks = [];
    let tableHookIdx = 0;
    const saved = {
      useState: React.useState,
      useRef: React.useRef,
      useId: React.useId,
      useCallback: React.useCallback,
      useEffect: React.useEffect,
    };
    React.useState = (initial) => {
      const idx = tableHookIdx++;
      if (!tableHooks[idx]) tableHooks[idx] = { value: typeof initial === "function" ? initial() : initial };
      return [
        tableHooks[idx].value,
        (u) => {
          tableHooks[idx].value = typeof u === "function" ? u(tableHooks[idx].value) : u;
        },
      ];
    };
    React.useId = () => {
      tableHookIdx++;
      return "tbl";
    };
    try {
      return tableEl.type(tableEl.props);
    } finally {
      Object.assign(React, saved);
    }
  }

  return {
    timers,
    decideCalls,
    loadCalls,
    toastMessages,
    flush,
    setPending: (next) => {
      pendingQueue = next;
    },
    setHistory: (next) => {
      historyQueue = next;
    },
    pendingTableProps: () =>
      elements(tree).find(
        (item) =>
          typeof item.type === "function" && item.type.name === "ApprovalTable" && item.props.label === "待处理审批",
      )?.props,
    renderPendingTable,
    async tickTimers(delay) {
      for (const [id, timer] of [...timers]) {
        if (timer.delay !== delay) continue;
        if (!timer.interval) timers.delete(id);
        timer.callback();
      }
      await flush();
    },
    async setHidden(hidden) {
      doc.hidden = hidden;
      for (const listener of [...visibilityListeners]) listener();
      await flush();
    },
    cleanup() {
      for (const h of hooks) h?.cleanup?.();
    },
  };
}

test("per-row lock allows concurrent approvals on distinct rows and coalesces background refreshes", async () => {
  const rowA = {
    id: 11,
    agent_name: "worker-1",
    tool_name: "bash",
    tool_input: { command: "ls" },
    status: "pending",
    reason: "needs check",
    created_at: new Date().toISOString(),
    timeout_enabled: true,
    timeout_seconds: 60,
  };
  const rowB = {
    id: 12,
    agent_name: "worker-2",
    tool_name: "bash",
    tool_input: { command: "pwd" },
    status: "pending",
    reason: "needs check",
    created_at: new Date().toISOString(),
    timeout_enabled: true,
    timeout_seconds: 45,
  };

  const h = queueHarness({ initialPending: [rowA, rowB], initialHistory: [rowA, rowB] });
  await h.flush();

  const initialLoads = h.loadCalls.pending;
  const tableProps = h.pendingTableProps();
  assert.ok(tableProps);
  assert.equal(tableProps.rows.length, 2);

  // Start deciding row 11, then immediately start deciding row 12 while row 11 is still in flight
  const p1 = tableProps.decide(11, "allowed");
  // Duplicate click on row 11 must be ignored while 11 is in flight
  const p1Dup = tableProps.decide(11, "allowed");
  const p2 = tableProps.decide(12, "denied");
  await h.flush();

  assert.equal(h.decideCalls.length, 2);
  assert.equal(h.decideCalls[0].id, 11);
  assert.equal(h.decideCalls[1].id, 12);

  const midProps = h.pendingTableProps();
  assert.equal(midProps.decidingIds.has(11), true);
  assert.equal(midProps.decidingIds.has(12), true);

  // Resolve row 11 first: optimistic removal drops 11, schedules coalesced 150ms refresh
  h.setPending([rowB]);
  h.decideCalls[0].resolve({ ok: true });
  await Promise.all([p1, p1Dup]);
  await h.flush();
  assert.deepEqual(
    h.pendingTableProps().rows.map((r) => r.id),
    [12],
  );
  assert.equal(h.loadCalls.pending, initialLoads);

  // Resolve row 12 quickly: coalesced 150ms timer resets instead of firing twice
  h.setPending([]);
  h.decideCalls[1].resolve({ ok: true });
  await p2;
  await h.flush();
  assert.equal(h.pendingTableProps(), undefined);
  assert.equal(h.loadCalls.pending, initialLoads);

  // Fire coalesced 150ms refresh -> only 1 background refresh occurs
  await h.tickTimers(150);
  assert.equal(h.loadCalls.pending, initialLoads + 1);
  // Zero 1000ms timer remains when pending list is empty
  assert.equal([...h.timers.values()].filter((t) => t.delay === 1000).length, 0);

  h.cleanup();
});

test("pending rows render inline Allow/Deny buttons with stopPropagation and countdown badge", async () => {
  const createdAt = new Date(Date.now() - 10_000).toISOString();
  const rowA = {
    id: 21,
    agent_name: "worker-1",
    tool_name: "bash",
    tool_input: { command: "id" },
    status: "pending",
    reason: "needs check",
    created_at: createdAt,
    timeout_enabled: true,
    timeout_seconds: 60,
  };
  const h = queueHarness({ initialPending: [rowA] });
  await h.flush();

  // 1s tick timer exists while pending.length > 0
  assert.equal([...h.timers.values()].filter((t) => t.delay === 1000).length, 1);

  const tableTree = h.renderPendingTable();
  const buttons = elements(tableTree).filter((el) => el.type === "Button");
  const allowBtn = buttons.find((b) => text(b.props.children).includes("允许"));
  const denyBtn = buttons.find((b) => text(b.props.children).includes("拒绝"));
  assert.ok(allowBtn, "inline 允许 button missing");
  assert.ok(denyBtn, "inline 拒绝 button missing");

  // Countdown badge is rendered
  const countdownBadge = elements(tableTree).find(
    (el) => el.type === "Badge" && /^\d+s$/.test(text(el.props.children)),
  );
  assert.ok(countdownBadge, "countdown badge missing");

  // Clicking inline Allow stops propagation and dispatches decide without toggling row expansion
  let stopped = false;
  const p = allowBtn.props.onClick({
    stopPropagation() {
      stopped = true;
    },
  });
  assert.equal(stopped, true);
  assert.equal(h.decideCalls.length, 1);
  assert.equal(h.decideCalls[0].id, 21);
  assert.equal(h.decideCalls[0].decision, "allowed");
  h.decideCalls[0].resolve({ ok: true });
  await p;
  await h.flush();
  h.cleanup();
});

test("background polling skips ticks while document.hidden and refreshes once on visibilitychange", async () => {
  const h = queueHarness({ initialHidden: true });
  await h.flush();
  const initialPendingLoads = h.loadCalls.pending;

  // 5s poll tick while hidden must be skipped
  await h.tickTimers(5000);
  assert.equal(h.loadCalls.pending, initialPendingLoads);

  // Becoming visible triggers a single immediate refresh
  await h.setHidden(false);
  assert.equal(h.loadCalls.pending, initialPendingLoads + 1);

  // Subsequent visible 5s poll tick runs normally
  await h.tickTimers(5000);
  assert.equal(h.loadCalls.pending, initialPendingLoads + 2);

  h.cleanup();
});
