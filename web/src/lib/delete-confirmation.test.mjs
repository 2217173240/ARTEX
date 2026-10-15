import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";
const require = createRequire(import.meta.url);
const React = require("react");
const source = readFileSync(new URL("../components/delete-confirmation.tsx", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, { compilerOptions: {
  target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS,
  jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true,
} }).outputText;
// Run the actual component callbacks with persistent hook slots. Radix focus and
// portal interactions require the separate real-browser fixture validation.
function harness(onConfirm) {
  const values = []; const errors = []; let cursor = 0;
  const hooks = {
    ...React,
    useState(initial) { const i = cursor++; if (!(i in values)) values[i] = initial; return [values[i], value => { values[i] = value; }]; },
    useRef(initial) { const i = cursor++; return values[i] ??= { current: initial }; },
  };
  const module = { exports: {} };
  runInNewContext(compiled, { module, exports: module.exports, require(name) {
    if (name === "react") return hooks;
    if (name === "sonner") return { toast: { error: text => errors.push(text) } };
    if (name === "@/lib/i18n") return { useI18n: () => ({ t: text => text }) };
    if (name === "@/components/ui/alert-dialog") return Object.fromEntries(
      ["AlertDialog", "AlertDialogTrigger", "AlertDialogContent", "AlertDialogHeader", "AlertDialogTitle", "AlertDialogDescription", "AlertDialogFooter", "AlertDialogCancel", "AlertDialogAction"].map(name => [name, name]),
    );
    return require(name);
  } });
  function render() {
    cursor = 0;
    return module.exports.DeleteConfirmation({ name: "raw target", description: "impact", onConfirm, children: React.createElement("button", null, "Delete") });
  }
  function find(tree, name) {
    if (!tree || typeof tree !== "object") return null;
    if (tree.type === name) return tree;
    for (const child of React.Children.toArray(tree.props?.children)) { const found = find(child, name); if (found) return found; }
    return null;
  }
  return { render, find, errors };
}
const click = node => node.props.onClick({ preventDefault() {} });
const settle = async () => { await Promise.resolve(); await Promise.resolve(); };
test("opening and closing without confirmation never delete", () => {
  let requests = 0; const h = harness(async () => { requests++; return true; });
  h.render().props.onOpenChange(true);
  const tree = h.render(); assert.equal(tree.props.open, true);
  h.find(tree, "AlertDialogContent").props.onEscapeKeyDown({ preventDefault() { assert.fail("idle Escape must close"); } });
  tree.props.onOpenChange(false); assert.equal(h.render().props.open, false); assert.equal(requests, 0);
});
test("pending blocks duplicate submission and closing, then success closes", async () => {
  let requests = 0; let finish;
  const h = harness(() => { requests++; return new Promise(resolve => { finish = resolve; }); });
  h.render().props.onOpenChange(true);
  let tree = h.render(); const action = h.find(tree, "AlertDialogAction"); click(action); click(action);
  tree = h.render(); assert.equal(requests, 1);
  assert.equal(h.find(tree, "AlertDialogAction").props.disabled, true);
  assert.equal(h.find(tree, "AlertDialogCancel").props.disabled, true);
  let prevented = false;
  h.find(tree, "AlertDialogContent").props.onEscapeKeyDown({ preventDefault() { prevented = true; } });
  assert.equal(prevented, true); tree.props.onOpenChange(false); assert.equal(h.render().props.open, true);
  finish(true); await settle(); assert.equal(h.render().props.open, false);
});
for (const throws of [false, true]) test(`${throws ? "unexpected rejection" : "reported failure"} keeps confirmation retryable`, async () => {
  let requests = 0; const h = harness(async () => { if (++requests > 1) return true; if (throws) throw new Error("fixture"); return false; });
  h.render().props.onOpenChange(true); click(h.find(h.render(), "AlertDialogAction")); await settle();
  let tree = h.render(); assert.equal(tree.props.open, true); assert.equal(h.find(tree, "AlertDialogAction").props.disabled, false);
  assert.equal(h.errors.length, throws ? 1 : 0);
  click(h.find(tree, "AlertDialogAction")); await settle(); assert.equal(requests, 2); assert.equal(h.render().props.open, false);
});
