import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

const compile = (path, modules) => {
  const source = readFileSync(new URL(path, import.meta.url), "utf8");
  const compiled = ts.transpileModule(source, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
  }).outputText;
  const module = { exports: {} };
  runInNewContext(compiled, { module, exports: module.exports, URLSearchParams, require: (name) => modules[name] });
  return module.exports;
};
const translate = (text, params = {}) => text.replace(/\{(\w+)\}/g, (match, key) => String(params[key] ?? match));
const elements = (node) => {
  if (Array.isArray(node)) return node.flatMap(elements);
  if (node && typeof node === "object") return [node, ...elements(node.props?.children)];
  return [];
};
const text = (node) => {
  if (Array.isArray(node)) return node.map(text).join("");
  if (node && typeof node === "object") return text(node.props?.children);
  return node == null || node === false ? "" : String(node);
};
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
};
const sameDeps = (a, b) => a && b && a.length === b.length && a.every((value, index) => Object.is(value, b[index]));

function harness(initialProps = { findingId: "1" }) {
  let props = initialProps;
  const hooks = [],
    effects = [],
    requests = [];
  let index = 0,
    dirty = true,
    tree;
  const React = {
    __esModule: true,
    useState(initial) {
      const i = index++;
      hooks[i] ??= { value: typeof initial === "function" ? initial() : initial };
      return [
        hooks[i].value,
        (next) => {
          hooks[i].value = typeof next === "function" ? next(hooks[i].value) : next;
          dirty = true;
        },
      ];
    },
    useRef(initial) {
      const i = index++;
      hooks[i] ??= { current: initial };
      return hooks[i];
    },
    useId() {
      index++;
      return "notes-input";
    },
    useCallback(callback, deps) {
      const i = index++;
      if (!sameDeps(hooks[i]?.deps, deps)) hooks[i] = { deps, value: callback };
      return hooks[i].value;
    },
    useEffect(callback, deps) {
      const i = index++;
      if (!sameDeps(hooks[i]?.deps, deps))
        effects.push(() => {
          hooks[i]?.cleanup?.();
          hooks[i] = { deps, cleanup: callback() };
        });
    },
  };
  const notes = Object.fromEntries(
    ["list", "create", "remove"].map((method) => [
      method,
      (...args) => {
        const request = { method, args, ...deferred() };
        requests.push(request);
        return request.promise;
      },
    ]),
  );
  const components = new Proxy({}, { get: (_target, key) => key });
  const modules = new Proxy(
    {
      react: React,
      "react/jsx-runtime": {
        jsx: (type, props, key) => ({ type, props, key }),
        jsxs: (type, props, key) => ({ type, props, key }),
      },
      "@/lib/finding-notes": {
        findingNotes: notes,
        FINDING_NOTE_LIMIT: 8000,
        findingNoteLength: (body) => Array.from(body).length,
      },
      "@/lib/i18n": { useI18n: () => ({ t: translate, locale: "en-US" }) },
      "@/lib/mock/enabled": { MOCK: false },
      "@/lib/status": { statusMeta: (_domain, value) => ({ label: value }) },
    },
    { get: (target, key) => target[key] ?? components },
  );
  const panel = compile("../components/finding-notes-panel.tsx", modules);
  async function flush() {
    for (let n = 0; n < 20; n++) {
      if (dirty) {
        dirty = false;
        index = 0;
        const wrapper = panel.FindingNotesPanel(props);
        tree = wrapper.type(wrapper.props);
        effects.splice(0).forEach((effect) => {
          effect();
        });
      }
      await Promise.resolve();
      await Promise.resolve();
      if (!dirty) return;
    }
    assert.fail("notes did not settle");
  }
  const node = (type, label) => elements(tree).find((item) => item.type === type && (!label || text(item) === label));
  return {
    requests,
    flush,
    nodes: () => elements(tree),
    text: () => text(tree),
    review: panel.FindingLastReview,
    async props(next) {
      const previousKey = panel.FindingNotesPanel(props).key;
      const nextKey = panel.FindingNotesPanel(next).key;
      if (previousKey !== nextKey) {
        hooks.forEach((hook) => {
          hook?.cleanup?.();
        });
        hooks.splice(0);
      }
      props = next;
      dirty = true;
      await flush();
    },
    async remove() {
      node("AlertDialogAction").props.onClick();
      await flush();
    },
    async resolve(i, value) {
      requests[i].resolve(value);
      await flush();
    },
    async reject(i) {
      requests[i].reject(new Error("archived task is read-only"));
      await flush();
    },
    async click(label) {
      node("Button", label).props.onClick();
      await flush();
    },
    async draft(body) {
      node("Textarea").props.onChange({ target: { value: body } });
      await flush();
    },
    async submit() {
      node("form").props.onSubmit({
        preventDefault() {
          /* Browser default is irrelevant to the harness. */
        },
      });
      await flush();
    },
    saveDisabled: () => node("Button", "保存备注")?.props.disabled,
    unmount() {
      hooks.forEach((hook) => {
        hook?.cleanup?.();
      });
    },
  };
}
const note = (id, body = "<script>raw evidence</script>\n  preserved") => ({
  id,
  author: "ARTEX (shared admin)",
  body,
  created_at: "2026-10-10T01:00:00Z",
});

test("notes client preserves raw bodies, context, cursor, and Unicode code point limit", async () => {
  const requests = [];
  const client = compile("./finding-notes.ts", {
    "@/lib/api": {
      http: async (...args) => {
        requests.push(args);
        return {};
      },
    },
    "@/lib/mock/enabled": { MOCK: false },
  });
  assert.equal(client.findingNoteLength("😀".repeat(8000)), 8000);
  assert.equal(client.findingNoteLength("😀".repeat(8001)), 8001);
  await client.findingNotes.list("a/b", "task & 2", 99);
  assert.match(requests[0][0], /^\/exploration\/findings\/a%2Fb\/notes\?/);
  const query = new URLSearchParams(requests[0][0].split("?")[1]);
  assert.equal(query.get("context_task"), "task & 2");
  assert.equal(query.get("before"), "99");
  assert.equal(query.get("limit"), "50");
  const body = "  <b>evidence</b>\n";
  await client.findingNotes.create("1", body, "2");
  assert.equal(JSON.parse(requests[1][1].body).body, body);
  await client.findingNotes.remove("1", "3", "2");
  assert.equal(requests[2][1].method, "DELETE");
  assert.match(requests[2][0], /\/notes\/3\?context_task=2$/);
});

test("failed list offers retry without empty state; older pagination retains raw evidence", async () => {
  const h = harness();
  await h.flush();
  await h.reject(0);
  assert.match(h.text(), /加载备注失败/);
  assert.doesNotMatch(h.text(), /暂无处置备注/);
  await h.click("重试");
  await h.resolve(1, { notes: [note("50")], has_more: true, next_before: 50 });
  assert.ok(h.nodes().some((n) => n.type === "p" && n.props.children === note("50").body));
  await h.click("加载更早备注");
  assert.equal(h.requests[2].args[2], 50);
  await h.resolve(2, { notes: [note("49")], has_more: false });
  assert.equal(h.nodes().filter((n) => n.type === "article").length, 2);
});

test("inherited context hides create and delete; unmounted reads cannot update state", async () => {
  const h = harness({ findingId: "1", contextTask: "2", readOnly: true });
  await h.flush();
  assert.equal(h.requests[0].args[1], "2");
  await h.resolve(0, { notes: [note("1")], has_more: false });
  assert.equal(
    h.nodes().some((n) => ["form", "AlertDialog", "Textarea"].includes(n.type)),
    false,
  );
  const late = harness();
  await late.flush();
  late.unmount();
  await late.resolve(0, { notes: [note("late")], has_more: false });
  assert.doesNotMatch(late.text(), /raw evidence/);
});

test("Unicode limit permits 8000 astral characters and mutation failure retains draft", async () => {
  const h = harness();
  await h.flush();
  await h.resolve(0, { notes: [], has_more: false });
  await h.draft("😀".repeat(8000));
  assert.equal(h.saveDisabled(), false);
  await h.draft("😀".repeat(8001));
  assert.equal(h.saveDisabled(), true);
  await h.draft("  <b>evidence</b>\n");
  await h.submit();
  assert.equal(h.requests[1].args[1], "  <b>evidence</b>\n");
  await h.reject(1);
  assert.match(h.text(), /archived task is read-only/);
  assert.equal(h.nodes().find((n) => n.type === "Textarea").props.value, "  <b>evidence</b>\n");
});

test("last manual review reports historical status after an automatic status change", () => {
  const h = harness();
  const result = h.review({
    finding: {
      status: "fixed",
      reviewed_by: "ARTEX (shared admin)",
      reviewed_at: "2026-10-10T01:00:00Z",
      reviewed_status: "confirmed",
    },
  });
  assert.match(text(result), /最后人工处置：共享 ARTEX 管理员于 .*标记为「confirmed」/);
  assert.doesNotMatch(text(result), /fixed/);
  assert.equal(h.review({ finding: { status: "confirmed" } }), null);
});

test("finding switches discard draft and ignore late list and mutation results", async () => {
  const h = harness();
  await h.flush();
  await h.props({ findingId: "2", contextTask: "3" });
  await h.resolve(0, { notes: [note("old")], has_more: false });
  assert.equal(
    h.nodes().some((n) => n.type === "article"),
    false,
  );
  await h.resolve(1, { notes: [], has_more: false });
  await h.draft("old finding draft");
  await h.submit();
  await h.props({ findingId: "3" });
  await h.resolve(2, note("late mutation"));
  assert.equal(
    h.nodes().some((n) => n.type === "article"),
    false,
  );
  assert.equal(h.nodes().find((n) => n.type === "Textarea").props.value, "");
  await h.resolve(3, { notes: [note("current")], has_more: false });
  assert.equal(h.nodes().filter((n) => n.type === "article").length, 1);
});

test("delete failures retain evidence and successful retry removes only the target", async () => {
  const h = harness();
  await h.flush();
  await h.resolve(0, { notes: [note("1"), note("2")], has_more: false });
  await h.remove();
  assert.equal(h.requests[1].method, "remove");
  assert.equal(h.requests[1].args[1], "1");
  await h.reject(1);
  assert.equal(h.nodes().filter((n) => n.type === "article").length, 2);
  await h.remove();
  await h.resolve(2);
  assert.equal(h.nodes().filter((n) => n.type === "article").length, 1);
});
