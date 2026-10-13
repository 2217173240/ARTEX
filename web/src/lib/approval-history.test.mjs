import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { runInNewContext } from "node:vm";

const lib = path.dirname(fileURLToPath(import.meta.url));
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
    setTimeout: (callback) => {
      callback();
      return 1;
    },
    require: (name) => loadTS(path.resolve(path.dirname(filename), `${name}.ts`), context, cache),
    ...context,
  });
  return module.exports;
}
const helpers = loadTS(path.join(lib, "approval-history.ts"));
const { approvalFilterError, approvalDecisionSource } = helpers;
const ids = (response) => Array.from(response.items, (row) => row.id);

const row = (extra = {}) => ({
  id: 1,
  status: "allowed",
  reason: "",
  tool_name: "Write",
  tool_input: {},
  agent_name: "worker",
  created_at: "2026-10-12T00:00:00Z",
  conv_title: "",
  conv_agent_key: "",
  rule_name: "",
  ...extra,
});

test("validation counts Unicode characters and validates exact IDs and real UTC days", () => {
  assert.equal(approvalFilterError({ q: ` ${"😀".repeat(200)} ` }), null);
  assert.match(approvalFilterError({ q: "😀".repeat(201) }), /200/);
  for (const q of ["#", "#0", "#01", "#-1", "#1e2", "#9223372036854775808"]) {
    assert.match(approvalFilterError({ q }), /编号/);
  }
  assert.equal(approvalFilterError({ q: "#9223372036854775807" }), null);
  for (const created_from of ["2026-02-29", "2026-02-30", "2026-10-1", "bad"]) {
    assert.match(approvalFilterError({ created_from }), /日期/);
  }
  assert.equal(approvalFilterError({ created_from: "2024-02-29", created_to: "2024-02-29" }), null);
  assert.match(approvalFilterError({ created_from: "2026-10-13", created_to: "2026-10-12" }), /晚于/);
});

test("only matching exact human audit evidence proves a legacy manual source", () => {
  assert.equal(
    approvalDecisionSource(row({ decision_source: "model" }), { decision_reason: "人工允许执行" }),
    "manual",
  );
  assert.equal(
    approvalDecisionSource(row({ status: "denied", rule_id: 1 }), { decision_reason: "人工拒绝执行" }),
    "manual",
  );
  for (const status of ["pending", "timeout", "denied"]) {
    assert.equal(
      approvalDecisionSource(row({ status, decision_source: "model" }), { decision_reason: "人工允许执行" }),
      "model",
    );
  }
  assert.equal(approvalDecisionSource(row(), { decision_reason: "人工允许，可能执行" }), "unknown");
  assert.equal(approvalDecisionSource(row({ decision_source: "unknown", rule_id: 1 })), "unknown");
});

function mockHarness() {
  const cache = new Map();
  const fixtures = loadTS(path.join(lib, "mock/data.ts"), {}, cache);
  fixtures.interceptHistory.push(
    row({
      id: 101,
      task_id: "t-acme-web",
      tool_input: { url: "https://literal.test/a?rate=100%", text: "needle_under \\hello" },
      created_at: "2026-07-25T23:59:59Z",
    }),
    row({ id: 102, task_id: "t-acme-web", reason: "NEEDLE plain", created_at: "2026-07-26T00:00:00Z" }),
    row({ id: 103, task_id: "t-acme-web", decision_source: "model", created_at: "2026-07-26T23:59:59Z" }),
    row({ id: 104, task_id: "t-acme-api", decision_source: "manual", created_at: "2026-07-27T00:00:00Z" }),
    row({ id: 105, task_id: "t-acme-web", created_at: "2026-07-26T23:30:00-01:00" }),
  );
  fixtures.interceptDetails[103] = { decision_reason: "人工允许执行", output: "Unique audit evidence" };
  return loadTS(path.join(lib, "mock/handler.ts"), {}, cache).mockHandle;
}

test("actual mock history has literal search, exact ID, UTC day, task and manual-source parity", async () => {
  const request = mockHarness();
  const get = (query, endpoint = "/intercept/history") => request("GET", `${endpoint}?${new URLSearchParams(query)}`);
  assert.deepEqual(ids(await get({ q: "100%" })), [101]);
  assert.deepEqual(ids(await get({ q: "needle_under" })), [101]);
  assert.deepEqual(ids(await get({ q: "\\hello" })), [101]);
  assert.deepEqual(ids(await get({ q: "#102" })), [102]);
  assert.deepEqual(ids(await get({ q: "NeEdLe" })), [101, 102]);
  assert.deepEqual(ids(await get({ q: "unique AUDIT" })), [103]);
  assert.ok(ids(await get({ q: "Acme 官网" })).includes(103));
  assert.deepEqual(
    ids(await get({ decision_source: "manual", created_from: "2026-07-26", created_to: "2026-07-26" })),
    [103],
  );
  assert.deepEqual(ids(await get({ decision_source: "manual" }, "/intercept/task/t-acme-web")), [103]);
  assert.deepEqual(ids(await get({ q: "#104" }, "/intercept/task/t-acme-web")), []);
  assert.deepEqual(ids(await get({ q: "#105", created_to: "2026-07-26" })), []);
  assert.deepEqual(ids(await get({ q: "#105", created_from: "2026-07-27", created_to: "2026-07-27" })), [105]);
  const paged = await get({ q: "needle", page: "2", size: "1" });
  assert.deepEqual(ids(paged), [102]);
  assert.equal(paged.total, 2);
  for (const query of [
    { q: "#01" },
    { q: "😀".repeat(201) },
    { created_from: "2026-02-30" },
    { created_from: "2026-07-27", created_to: "2026-07-26" },
  ]) {
    await assert.rejects(get(query));
  }
  await request("POST", "/intercept/pending/93/decide", JSON.stringify({ decision: "allowed" }));
  assert.ok(ids(await get({ decision_source: "manual" })).includes(93));
  const detail = await request("GET", "/intercept/history/93");
  assert.equal(detail.decision_source, "manual");
  assert.match(detail.audit.initial_reason, /^\[模型\]/);
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

function pageHarness(initialTask) {
  let taskId = initialTask;
  const hooks = [],
    effects = [],
    timers = new Map(),
    requests = [],
    pendingRequests = [];
  let hookIndex = 0,
    timerId = 0,
    dirty = true,
    tree;
  const React = {
    useState(initial) {
      const i = hookIndex++;
      hooks[i] ??= { value: initial };
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
      return "filters";
    },
    useCallback(callback, deps) {
      const i = hookIndex++;
      if (!sameDeps(hooks[i]?.deps, deps)) hooks[i] = { deps, callback };
      return hooks[i].callback;
    },
    useEffect(callback, deps) {
      const i = hookIndex++,
        before = hooks[i];
      if (!sameDeps(before?.deps, deps)) {
        hooks[i] = { deps, cleanup: before?.cleanup };
        effects.push(() => {
          hooks[i].cleanup?.();
          hooks[i].cleanup = callback();
        });
      }
    },
  };
  const deferred = (list, extra = {}) => {
    let resolve, reject;
    const promise = new Promise((yes, no) => {
      resolve = yes;
      reject = no;
    });
    list.push({ ...extra, resolve, reject });
    return promise;
  };
  const api = {
    interceptHistoryPage: (page, size, filter) => deferred(requests, { page, size, filter }),
    interceptTaskPage: (scope, page, size, filter) => deferred(requests, { scope, page, size, filter }),
    interceptPending: () => deferred(pendingRequests),
  };
  const schedule = (callback, delay, interval = false) => {
    const id = ++timerId;
    timers.set(id, { callback, delay, interval });
    return id;
  };
  const components = new Proxy({}, { get: (_, name) => name });
  const module = loadTS(path.join(lib, "../components/approval-records.tsx"), {
    require: (name) =>
      ({
        react: React,
        "react/jsx-runtime": { jsx: (type, props) => ({ type, props }), jsxs: (type, props) => ({ type, props }) },
        "@/lib/api": { api },
        "@/lib/approval-history": helpers,
        "@/lib/i18n": { useI18n: () => ({ t: translateUI }) },
        "@/lib/utils": { cn: (...values) => values.filter(Boolean).join(" ") },
        sonner: { toast: { success: () => undefined, error: () => undefined } },
      })[name] ?? components,
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
        tree = module.ApprovalRecords({ taskId });
        effects.splice(0).forEach((effect) => {
          effect();
        });
      }
      await Promise.resolve();
    }
    assert.equal(dirty, false);
  }
  const find = (type, predicate = () => true) => {
    const node = elements(tree).find(
      (item) => (typeof item.type === "function" ? item.type.name : item.type) === type && predicate(item.props),
    );
    assert.ok(node, `missing ${type}`);
    return node.props;
  };
  return {
    requests,
    pendingRequests,
    flush,
    rows: () =>
      elements(tree)
        .filter((item) => item.type?.name === "ApprovalTable" && item.props.label === "审批记录列表")
        .flatMap((item) => Array.from(item.props.rows, (item) => item.id)),
    pending: () => find("ApprovalTable", (props) => props.label === "待处理审批").rows,
    text: () => text(tree),
    input: (suffix) => find("Input", (props) => props.id.endsWith(suffix)),
    async change(suffix, value) {
      find("Input", (props) => props.id.endsWith(suffix)).onChange({ target: { value } });
      await flush();
    },
    async source(value) {
      find(
        "Select",
        (props) => props.value === "all" && elements(props.children).some((item) => item.props.value === "manual"),
      ).onValueChange(value);
      await flush();
    },
    async page(value) {
      find("TablePagination").onPageChange(value);
      await flush();
    },
    async task(value) {
      taskId = value;
      dirty = true;
      await flush();
    },
    async clear() {
      find("Button", (props) => text(props.children) === "清除筛选").onClick();
      await flush();
    },
    async timers(delay) {
      for (const [id, timer] of [...timers]) {
        if (timer.delay !== delay) continue;
        if (!timer.interval) timers.delete(id);
        timer.callback();
      }
      await flush();
    },
    async resolve(index, items = [], total = 100) {
      requests[index].resolve({ items, total });
      pendingRequests.forEach((request) => {
        request.resolve([]);
      });
      await flush();
    },
    async pendingResolve(items) {
      pendingRequests.forEach((request) => {
        request.resolve(items);
      });
      await flush();
    },
  };
}

test("search debounces, resets page, and discards old responses before the debounce completes", async () => {
  const page = pageHarness();
  await page.flush();
  await page.resolve(0, [row()]);
  await page.page(3);
  await page.change("-search", " needle ");
  assert.deepEqual(page.rows(), []);
  await page.resolve(1, [row({ id: 999 })]);
  assert.deepEqual(page.rows(), []);
  assert.equal(page.requests.length, 2);
  await page.change("-search", " final ");
  await page.timers(300);
  assert.equal(page.requests.length, 3);
  assert.equal(page.requests[2].page, 1);
  assert.equal(page.requests[2].filter.q, "final");
  await page.resolve(2, [row({ id: 2 })]);
  assert.deepEqual(page.rows(), [2]);
  await page.change("-search", "final   ");
  assert.deepEqual(page.rows(), [2]);
  assert.equal(page.input("-search").maxLength, undefined);
});

test("invalid search and date ranges skip history requests while keeping pending approvals usable", async () => {
  const page = pageHarness("t-acme-web");
  await page.flush();
  await page.resolve(0);
  await page.change("-from", "2026-10-13");
  await page.change("-to", "2026-10-12");
  const count = page.requests.length;
  await page.pendingResolve([row({ id: 7, task_id: "t-acme-web", status: "pending" })]);
  assert.match(page.text(), /开始日期不能晚于结束日期/);
  assert.equal(page.input("-to")["aria-invalid"], true);
  assert.equal(page.pending()[0].id, 7);
  await page.timers(5000);
  assert.equal(page.requests.length, count);
  await page.change("-to", "2026-10-13");
  assert.equal(page.requests.at(-1).filter.created_from, "2026-10-13");
  assert.equal(page.requests.at(-1).filter.created_to, "2026-10-13");
  await page.change("-search", "#01");
  await page.timers(300);
  assert.match(page.text(), /编号必须/);
  assert.equal(page.requests.length, count + 1);
  await page.clear();
  assert.equal(page.input("-search").value, "");
  assert.equal(page.input("-from").value, "");
  assert.equal(Object.keys(page.requests.at(-1).filter).length, 0);
});

test("filters apply per task, manual source resets page, task switches reject stale results", async () => {
  const page = pageHarness("t-acme-web");
  await page.flush();
  await page.resolve(0);
  await page.page(3);
  await page.source("manual");
  assert.equal(page.requests.at(-1).page, 1);
  assert.equal(page.requests.at(-1).filter.decision_source, "manual");
  const stale = page.requests.length - 1;
  await page.task("t-acme-api");
  assert.equal(page.requests.at(-1).scope, "t-acme-api");
  await page.resolve(stale, [row({ id: 99 })]);
  assert.deepEqual(page.rows(), []);
  await page.resolve(page.requests.length - 1, [row({ id: 8 })]);
  assert.deepEqual(page.rows(), [8]);
});
