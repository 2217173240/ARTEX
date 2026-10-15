import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { runInNewContext } from "node:vm";
import test from "node:test";
import ts from "typescript";
const require = createRequire(import.meta.url);
const cache = new Map();
function load(url) {
  if (cache.has(url.href)) return cache.get(url.href);
  const module = { exports: {} };
  cache.set(url.href, module.exports);
  const source = ts.transpileModule(readFileSync(url, "utf8"), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText;
  const localRequire = (name) => name.startsWith(".") ? load(new URL(`${name}.ts`, url)) : require(name);
  runInNewContext(source, { module, exports: module.exports, require: localRequire, URL, URLSearchParams, structuredClone, setTimeout, clearTimeout, setInterval, clearInterval, console });
  return module.exports;
}
const helper = load(new URL("./mcp-approval.ts", import.meta.url));
const { mockHandle } = load(new URL("./mock/handler.ts", import.meta.url));
const call = (method, path, body) => mockHandle(method, path, body == null ? undefined : JSON.stringify(body));
test("argv retains spaces and rejects ambiguous argument text", () => {
  assert.equal(JSON.stringify(helper.parseMcpArgs('["--name", "two words", ""]')), '["--name","two words",""]');
  assert.throws(() => helper.parseMcpArgs("--name two words"));
  assert.throws(() => helper.parseMcpArgs('[1]'));
});
test("approval destination hides URL credential values", () => {
  const summary = helper.mcpApprovalDestination({ transport: "http", url: "https://user:secret@example.test:9443/mcp?token=secret&other=private#secret" });
  assert.match(summary, /example.test:9443\/mcp/);
  assert.match(summary, /token=/);
  assert.doesNotMatch(summary, /secret|private|user/);
});
test("mock saves drafts, enforces revisions and gates live discovery", async () => {
  const draft = await call("POST", "/mcp", { name: "approval-test", transport: "stdio", command: "synthetic", args: ["two words"], env: { TEST_SECRET: "synthetic-value" }, enabled: true, approved: true });
  assert.equal(draft.approved, false);
  assert.equal(draft.enabled, false);
  await assert.rejects(call("POST", `/mcp/${draft.id}/refresh`, {}));
  const approved = await call("POST", `/mcp/${draft.id}/approve`, { revision: draft.revision });
  assert.equal(approved.enabled, true);
  await call("POST", `/mcp/${draft.id}/refresh`, {});
  await assert.rejects(call("POST", `/mcp/${draft.id}/approve`, { revision: draft.revision }), { status: 409 });
  const listed = (await call("GET", "/mcp")).servers.find((row) => row.id === draft.id);
  assert.equal(listed.env, undefined);
  assert.equal(JSON.stringify(listed.env_keys), '["TEST_SECRET"]');
  const retained = await call("POST", "/mcp", { ...listed });
  assert.equal(retained.approved, true);
  assert.equal(retained.revision, approved.revision);
  const changed = await call("POST", "/mcp", { ...listed, revision: retained.revision, env: { NEW_KEY: "other-synthetic" } });
  assert.equal(changed.approved, false);
  assert.equal(changed.enabled, false);
  const reapproved = await call("POST", `/mcp/${draft.id}/approve`, { revision: changed.revision });
  const disabled = await call("POST", `/mcp/${draft.id}/disable`, { revision: reapproved.revision });
  assert.equal(disabled.approved, false);
  await assert.rejects(call("POST", `/mcp/${draft.id}/refresh`, {}));
});
test("ScopeSentry saves only a draft and retains credentials on URL-only save", async () => {
  const draft = await call("POST", "/sync/scopesentry/datasource", { url: "https://example.test/mcp", api_key: "synthetic" });
  const status = await call("GET", "/sync/scopesentry/status");
  assert.equal(status.id, draft.id);
  assert.equal(status.approved, false);
  assert.equal(status.configured, true);
  await assert.rejects(call("GET", "/sync/scopesentry/projects"));
  await call("POST", "/sync/scopesentry/datasource", { url: status.url, revision: status.revision });
  assert.equal((await call("GET", "/sync/scopesentry/status")).configured, true);
});

test("actual save callback sends CAS, exact argv and omitted credentials without approval", async () => {
  const page = readFileSync(new URL("../app/(main)/system/mcp/page.tsx", import.meta.url), "utf8");
  const ast = ts.createSourceFile("page.tsx", page, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const component = ast.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "MCPPage");
  const callback = component.body.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "saveForm");
  const source = ts.createPrinter().printNode(ts.EmitHint.Unspecified, callback, ast);
  const sent = [];
  const form = { name: "test", transport: "stdio", command: "synthetic", args: '["two words", ""]', env: "", clearEnv: false };
  const context = {
    form, editing: { id: 9, revision: 4, approved: true, enabled: true, env_keys: ["KEY"] },
    saveLock: { current: false }, mutationLock: { current: false }, savedForm: { current: "" },
    setSaving() {}, setEditing() {}, setForm() {}, loadTools() {}, load() {},
    toast: { success() {}, error(message) { throw new Error(message); } },
    uiText: (text) => text, parseMcpArgs: helper.parseMcpArgs, parseEnv: () => ({}),
    ApiError: class extends Error {},
    api: { saveMcpServer: async (payload) => { sent.push(payload); return { id: 9, revision: 5, approved: true, enabled: true }; } },
  };
  const js = ts.transpileModule(`${source}\nglobalThis.run = saveForm;`, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;
  runInNewContext(js, context);
  await context.run();
  assert.equal(sent.length, 1);
  assert.equal(sent[0].revision, 4);
  assert.equal(JSON.stringify(sent[0].args), '["two words",""]');
  assert.equal("env" in sent[0], false);
  assert.equal("approved" in sent[0], false);
  assert.equal("enabled" in sent[0], false);
  form.clearEnv = true;
  await context.run();
  assert.equal(JSON.stringify(sent[1].env), "{}");
});


test("mock ignores env object key order but treats argv order as a config change", async () => {
  const config = { name: "key-order-test", transport: "stdio", command: "synthetic", args: ["one", "two"], env: { FIRST: "synthetic-a", SECOND: "synthetic-b" } };
  const saved = await call("POST", "/mcp", config);
  const approved = await call("POST", `/mcp/${saved.id}/approve`, { revision: saved.revision });
  const reordered = await call("POST", "/mcp", { ...config, id: saved.id, revision: approved.revision, env: { SECOND: "synthetic-b", FIRST: "synthetic-a" } });
  assert.equal(reordered.revision, approved.revision);
  assert.equal(reordered.approved, true);
  assert.equal(reordered.enabled, true);
  const changed = await call("POST", "/mcp", { ...config, id: saved.id, revision: reordered.revision, args: ["two", "one"] });
  assert.equal(changed.revision, reordered.revision + 1);
  assert.equal(changed.approved, false);
  assert.equal(changed.enabled, false);
});

test("actual approval dialog ignores pending close and Escape requests; failure remains retryable", async () => {
  const page = readFileSync(new URL("../app/(main)/system/mcp/page.tsx", import.meta.url), "utf8");
  const ast = ts.createSourceFile("page.tsx", page, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const callbacks = {};
  function visit(node) {
    if (ts.isJsxOpeningElement(node) && ["AlertDialog", "AlertDialogContent"].includes(node.tagName.getText(ast))) {
      for (const attribute of node.attributes.properties) {
        if (ts.isJsxAttribute(attribute) && ["onOpenChange", "onEscapeKeyDown"].includes(attribute.name.text)) {
          callbacks[attribute.name.text] = attribute.initializer.expression.getText(ast);
        }
      }
    }
    ts.forEachChild(node, visit);
  }
  visit(ast);
  let approval = { id: 1, revision: 3 };
  let errors = 0;
  const context = { mutationLock: { current: true }, saveLock: { current: false }, editing: null,
    setApproval(value) { approval = value; }, setMutating() {}, load() {},
    api: { approveMcpServer: async () => { throw new Error("synthetic failure"); } },
    toast: { error() { errors++; }, success() {} }, uiText: value => value,
  };
  runInNewContext(`globalThis.close = ${callbacks.onOpenChange}; globalThis.escape = ${callbacks.onEscapeKeyDown};`, context);
  context.close(false);
  assert.ok(approval);
  let prevented = false;
  context.escape({ preventDefault() { prevented = true; } });
  assert.equal(prevented, true);
  const component = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === "MCPPage");
  const mutation = component.body.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === "mutateServer");
  const code = ts.transpileModule(`${mutation.getText(ast)}; globalThis.mutate = mutateServer;`, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;
  runInNewContext(code, context);
  context.mutationLock.current = false;
  await context.mutate(approval, true);
  assert.ok(approval);
  assert.equal(errors, 1);
  assert.equal(context.mutationLock.current, false);
  context.close(false);
  assert.equal(approval, null);
});

test("actual approval callbacks restore the connected card or sheet launcher on close", async () => {
  const page = readFileSync(new URL("../app/(main)/system/mcp/page.tsx", import.meta.url), "utf8");
  const ast = ts.createSourceFile("page.tsx", page, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const component = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === "MCPPage");
  const request = component.body.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === "requestApproval");
  let close;
  function visit(node) {
    if (ts.isJsxOpeningElement(node) && node.tagName.getText(ast) === "AlertDialogContent") {
      close = node.attributes.properties.find(attribute => ts.isJsxAttribute(attribute) && attribute.name.text === "onCloseAutoFocus")?.initializer.expression.getText(ast);
    }
    ts.forEachChild(node, visit);
  }
  visit(ast);
  class Element {
    isConnected = true;
    focuses = 0;
    focus(options) { this.focuses++; assert.equal(options.preventScroll, true); }
  }
  const body = new Element();
  const context = { document: { activeElement: null, body }, HTMLElement: Element,
    mutationLock: { current: false }, saveLock: { current: false }, approvalLauncherRef: { current: null },
    setMutating() {}, setApproval() {}, uiText: value => value, toast: { error() {} },
    api: { mcpServers: async () => [{ id: 42, revision: 1 }] },
  };
  const source = `${request.getText(ast)}; globalThis.request = requestApproval; globalThis.close = ${close};`;
  runInNewContext(ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context);
  for (const launcher of [new Element(), new Element()]) {
    context.document.activeElement = launcher;
    await context.request({ id: 42 });
    assert.equal(context.approvalLauncherRef.current, launcher);
    let prevented = false;
    context.close({ preventDefault() { prevented = true; } });
    assert.equal(prevented, true);
    assert.equal(launcher.focuses, 1);
    launcher.isConnected = false;
    context.close({ preventDefault() {} });
    assert.equal(launcher.focuses, 1);
  }
  context.document.activeElement = body;
  await context.request({ id: 42 });
  assert.equal(context.approvalLauncherRef.current, null);
});
