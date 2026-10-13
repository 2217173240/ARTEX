import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

const source = readFileSync(
  new URL("../app/(main)/function/tasks/detail/_tabs/report-tab.tsx", import.meta.url),
  "utf8",
);
const compiled = ts.transpileModule(source, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
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

function reportHarness(initialTask = "one") {
  const requests = [],
    copies = [],
    effects = [];
  let hooks = [],
    index = 0,
    dirty = true,
    tree,
    taskId = initialTask,
    mountedKey;
  const React = {
    useState(initial) {
      const i = index++;
      hooks[i] ??= { value: initial };
      const slot = hooks[i];
      return [
        slot.value,
        (update) => {
          const next = typeof update === "function" ? update(slot.value) : update;
          if (!Object.is(next, slot.value)) {
            slot.value = next;
            dirty = true;
          }
        },
      ];
    },
    useEffect(callback, deps) {
      const i = index++,
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
  const components = new Proxy({}, { get: (_, name) => name });
  const api = {
    report(scope) {
      let resolve, reject;
      const promise = new Promise((yes, no) => {
        resolve = yes;
        reject = no;
      });
      requests.push({ scope, resolve, reject });
      return promise;
    },
  };
  const module = { exports: {} };
  runInNewContext(compiled, {
    module,
    exports: module.exports,
    require: (name) =>
      ({
        react: React,
        "react/jsx-runtime": {
          jsx: (type, props, key) => ({ type, props, key }),
          jsxs: (type, props, key) => ({ type, props, key }),
        },
        "@/lib/api": { api },
        "@/lib/i18n": { useI18n: () => ({ t: (value) => value }) },
        "@/lib/utils": {
          copyText: async (value) => {
            copies.push(value);
            return true;
          },
        },
        sonner: { toast: { success: () => undefined, error: () => undefined } },
      })[name] ?? components,
    setTimeout: () => 1,
  });
  function render() {
    const child = module.exports.ReportTab({ taskId });
    if (mountedKey !== child.key) {
      hooks.forEach((hook) => {
        hook.cleanup?.();
      });
      hooks = [];
      mountedKey = child.key;
    }
    index = 0;
    dirty = false;
    tree = child.type(child.props);
  }
  async function flush() {
    for (let turn = 0; turn < 20; turn++) {
      if (dirty) {
        render();
        effects.splice(0).forEach((effect) => {
          effect();
        });
      }
      await Promise.resolve();
    }
    assert.equal(dirty, false);
  }
  return {
    requests,
    copies,
    flush,
    text: () => text(tree),
    roles: () =>
      elements(tree)
        .map((node) => node.props.role)
        .filter(Boolean),
    report: () => elements(tree).find((node) => node.type === "Markdown")?.props.text,
    copyVisible: () => elements(tree).some((node) => node.type === "Button" && text(node).trim() === "复制"),
    async copy() {
      await elements(tree)
        .find((node) => node.type === "Button" && text(node).trim() === "复制")
        .props.onClick();
      await flush();
    },
    async retry() {
      elements(tree)
        .find((node) => node.type === "Button" && text(node) === "重试")
        .props.onClick();
      await flush();
    },
    async resolve(i, value) {
      requests[i].resolve(value);
      await flush();
    },
    async reject(i) {
      requests[i].reject(new Error("report fetch failed"));
      await flush();
    },
    async task(value) {
      taskId = value;
      dirty = true;
      render();
      effects.splice(0).forEach((effect) => {
        effect();
      });
      return flush();
    },
    unmount() {
      hooks.forEach((hook) => {
        hook.cleanup?.();
      });
    },
  };
}

test("failed fetch has an accessible error and retry; successful empty report remains distinct", async () => {
  const report = reportHarness();
  await report.flush();
  assert.ok(report.roles().includes("status"));
  await report.reject(0);
  assert.match(report.text(), /报告加载失败，请重试。/);
  assert.doesNotMatch(report.text(), /暂无报告/);
  assert.ok(report.roles().includes("alert"));
  assert.equal(report.copyVisible(), false);
  await report.retry();
  assert.ok(report.roles().includes("status"));
  assert.equal(report.requests.length, 2);
  await report.resolve(1, "");
  assert.match(report.text(), /暂无报告/);
  assert.equal(report.roles().includes("alert"), false);
  assert.equal(report.copyVisible(), false);
});

test("a recovered report is rendered and copied verbatim", async () => {
  const report = reportHarness();
  await report.flush();
  await report.reject(0);
  await report.retry();
  const content = "# Raw report\n\n中文用户证据";
  await report.resolve(1, content);
  assert.equal(report.report(), content);
  assert.equal(report.copyVisible(), true);
  await report.copy();
  assert.deepEqual(report.copies, [content]);
});

test("task key removes previous content and copy immediately, and stale responses cannot replace the new report", async () => {
  const report = reportHarness();
  await report.flush();
  await report.resolve(0, "old report");
  await report.task("two");
  assert.equal(report.report(), undefined);
  assert.equal(report.copyVisible(), false);
  assert.equal(report.requests[1].scope, "two");
  await report.task("three");
  await report.resolve(2, "current report");
  await report.resolve(1, "stale report");
  assert.equal(report.report(), "current report");
  await report.copy();
  assert.deepEqual(report.copies, ["current report"]);
});

test("late failures from an unmounted task do not change the current task", async () => {
  const report = reportHarness();
  await report.flush();
  await report.task("two");
  await report.resolve(1, "current report");
  await report.reject(0);
  assert.equal(report.report(), "current report");
  assert.equal(report.roles().includes("alert"), false);
  report.unmount();
});
