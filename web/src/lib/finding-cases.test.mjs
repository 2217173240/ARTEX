import ts from "typescript";

const translateUI = (text, params = {}) => text.replace(/\{(\w+)\}/g, (match, key) => String(params[key] ?? match));

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

const compiled = ts.transpileModule(
  readFileSync(new URL("../components/finding-case-list.tsx", import.meta.url), "utf8"),
  {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
  },
).outputText;
const elements = (node) =>
  Array.isArray(node)
    ? node.flatMap(elements)
    : node && typeof node === "object"
      ? [node, ...elements(node.props?.children)]
      : [];
const text = (node) =>
  Array.isArray(node)
    ? node.map(text).join("")
    : node && typeof node === "object"
      ? text(node.props?.children)
      : node == null || node === false
        ? ""
        : String(node);
function harness(component, initialProps) {
  let props = initialProps,
    index = 0,
    dirty = true,
    tree;
  const hooks = [],
    effects = [],
    requests = [],
    timers = new Map();
  const React = {
    useState(initial) {
      const i = index++;
      hooks[i] ??= { value: typeof initial === "function" ? initial() : initial };
      return [
        hooks[i].value,
        (update) => {
          const value = typeof update === "function" ? update(hooks[i].value) : update;
          if (!Object.is(value, hooks[i].value)) {
            hooks[i].value = value;
            dirty = true;
          }
        },
      ];
    },
    useEffect(callback, deps) {
      const i = index++,
        old = hooks[i];
      if (!old || deps.some((v, j) => !Object.is(v, old.deps[j]))) {
        hooks[i] = { deps, cleanup: old?.cleanup };
        effects.push(() => {
          hooks[i].cleanup?.();
          hooks[i].cleanup = callback();
        });
      }
    },
  };
  const api = new Proxy(
    {},
    {
      get:
        (_, method) =>
        (...args) => {
          let resolve, reject;
          const promise = new Promise((yes, no) => {
            resolve = yes;
            reject = no;
          });
          requests.push({ method, args, resolve, reject });
          return promise;
        },
    },
  );
  const components = new Proxy({}, { get: (_, name) => name });
  const module = { exports: {} };
  runInNewContext(compiled, {
    module,
    exports: module.exports,
    require: (name) =>
      ({
        react: React,
        "react/jsx-runtime": { jsx: (type, props) => ({ type, props }), jsxs: (type, props) => ({ type, props }) },
        "@/lib/api": { api },
        "@/lib/i18n": {
          useI18n: () => ({
            t: translateUI,
          }),
        },
        "@/lib/utils": { cn: (...v) => v.filter(Boolean).join(" ") },
      })[name] ?? components,
    setTimeout: (callback) => {
      const id = timers.size + 1;
      timers.set(id, callback);
      return id;
    },
    clearTimeout: (id) => timers.delete(id),
  });
  async function flush() {
    for (let n = 0; n < 20; n++) {
      if (dirty) {
        dirty = false;
        index = 0;
        tree = module.exports[component](props);
        effects.splice(0).forEach((effect) => {
          effect();
        });
      }
      await Promise.resolve();
      await Promise.resolve();
      if (!dirty) return;
    }
    assert.fail("render did not settle");
  }
  return {
    requests,
    flush,
    text: () => text(tree),
    nodes: () => elements(tree),
    async props(next) {
      props = next;
      dirty = true;
      await flush();
    },
    async resolve(i, value) {
      requests[i].resolve(value);
      await flush();
    },
    async reject(i) {
      requests[i].reject(new Error("offline"));
      await flush();
    },
    async poll() {
      [...timers.values()].forEach((callback) => {
        callback();
      });
      timers.clear();
      await flush();
    },
    unmount() {
      hooks.forEach((hook) => {
        hook?.cleanup?.();
      });
    },
  };
}
const finding = (id) => ({ id, finding_id: id, name: id, severity: "high", status: "pending" });
const page = (id, total = 1) => ({
  items: [{ finding: finding(id), matched_ids: [] }],
  total,
  matched_reports: total + 10,
  stats: { total: 100, reports: 200, unassessed: 0 },
});

test("case query changes hide old snapshots and late successes cannot restore them", async () => {
  const totals = [],
    onTotal = (total) => totals.push(total),
    h = harness("FindingCaseList", { query: { severity: "all" }, readOnly: true, onTotal });
  await h.flush();
  await h.resolve(0, page("first"));
  await h.poll();
  await h.props({ query: { severity: "low" }, readOnly: true, onTotal });
  assert.equal(
    h.nodes().some((n) => n.props?.finding?.finding_id === "first"),
    false,
  );
  assert.equal(totals.at(-1), 0);
  await h.resolve(1, page("obsolete"));
  assert.equal(
    h.nodes().some((n) => n.props?.finding?.finding_id === "obsolete"),
    false,
  );
  await h.resolve(2, page("current"));
  assert.equal(
    h.nodes().some((n) => n.props?.finding?.finding_id === "current"),
    true,
  );
  assert.equal(totals.at(-1), 11);
  assert.match(h.text(), /当前筛选：1 个独立漏洞 · 11 条上报/);
  assert.doesNotMatch(h.text(), /100 个独立漏洞|200 条上报/);
});

test("case refresh failures retain evidence and offer retry; unmounted requests are ignored", async () => {
  const h = harness("FindingCaseList", { query: {}, readOnly: true });
  await h.flush();
  await h.resolve(0, page("retained"));
  await h.poll();
  await h.reject(1);
  assert.equal(
    h.nodes().some((n) => n.props?.finding?.finding_id === "retained"),
    true,
  );
  assert.match(h.text(), /offline/);
  h.nodes()
    .find((n) => n.type === "Button" && text(n) === "重试")
    .props.onClick();
  await h.flush();
  h.unmount();
  await h.resolve(2, page("late"));
  assert.equal(
    h.nodes().some((n) => n.props?.finding?.finding_id === "late"),
    false,
  );
});

test("member pagination hides earlier pages and ignores old failures", async () => {
  const h = harness("FindingCaseMembers", { caseId: "c1" });
  await h.flush();
  await h.resolve(0, { items: [finding("first")], total: 40 });
  h.nodes()
    .find((n) => n.type === "TablePagination")
    .props.onPageChange(2);
  await h.flush();
  assert.equal(
    h.nodes().some((n) => n.props?.finding?.finding_id === "first"),
    false,
  );
  await h.props({ caseId: "c2" });
  await h.reject(1);
  assert.doesNotMatch(h.text(), /offline/);
  const latest = h.requests.length - 1;
  await h.resolve(latest, { items: [finding("new")], total: 1 });
  assert.equal(
    h.nodes().some((n) => n.props?.finding?.finding_id === "new"),
    true,
  );
});

test("nonmatching members and inherited reports cannot be selected", async () => {
  for (const props of [{ matched: false }, { finding: { ...finding("1"), inherited: true } }]) {
    const h = harness("FindingCaseMemberRow", {
      finding: finding("1"),
      onSelect() {
        /* Selection is intentionally unavailable. */
      },
      ...props,
    });
    await h.flush();
    assert.equal(
      h.nodes().some((n) => n.type === "Checkbox"),
      false,
    );
  }
});

test("member mutation refresh reloads evidence even when the folder version is unchanged", async () => {
  const rendered = [];
  const renderRecords = (items) => {
    rendered.push(items.map((item) => item.finding_id));
    return { type: "records", props: { children: items.map((item) => item.name) } };
  };
  const h = harness("FindingCaseMembers", { caseId: "42", version: 1, refreshToken: 0, renderRecords });
  await h.flush();
  await h.resolve(0, { items: [finding("7")], total: 1 });
  await h.props({ caseId: "42", version: 1, refreshToken: 1, renderRecords });
  assert.equal(h.requests.length, 2);
  await h.reject(1);
  assert.match(h.text(), /offline/);
  assert.deepEqual(rendered.at(-1), ["7"]);
});
