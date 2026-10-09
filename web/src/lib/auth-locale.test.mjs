import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import vm from "node:vm";
import ts from "typescript";

const english = JSON.parse(fs.readFileSync(new URL("./i18n/en.json", import.meta.url), "utf8"));
function authHarness(pageName, values = {}, callbackName = "handleSubmit") {
  const source = fs.readFileSync(new URL(`../app/(auth)/${pageName}/page.tsx`, import.meta.url), "utf8");
  const tree = ts.createSourceFile("page.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  let submit;
  let errorRenderer;
  function visit(node) {
    if (ts.isFunctionDeclaration(node) && node.name?.text === callbackName) submit = node;
    if (ts.isConditionalExpression(node) && node.condition.getText(tree) === '"key" in error') errorRenderer = node;
    ts.forEachChild(node, visit);
  }
  visit(tree);
  assert.ok(submit, "actual submit handler must exist");
  assert.ok(errorRenderer, "actual error renderer must distinguish UI keys from server messages");
  let error = null;
  let locale = "zh-CN";
  const translate = (key) => locale === "en" ? english[key] ?? key : key;
  const handler = vm.runInNewContext(`${ts.transpileModule(submit.getText(tree), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText}\n${callbackName}`, {
    agreed: false, password: "abc", confirm: "different",
    setError: (value) => { error = value; }, setLoading: () => {}, setChecking: () => {},
    api: { initPassword: async () => {} }, auth: { signedIn: async () => {} }, router: { replace: () => {} },
    t: translate, uiText: translate, Error, ...values,
  });
  return {
    submit: () => handler({ preventDefault() {} }),
    setLocale: (next) => { locale = next; },
    renderError: () => vm.runInNewContext(errorRenderer.getText(tree), { error, t: translate, uiText: translate }),
  };
}

for (const [page, key, values] of [
  ["login", "请先阅读并同意《使用须知》", {}],
  ["setup", "两次输入的密码不一致", {}],
  ["setup", "密码长度至少 8 位", { password: "abc", confirm: "abc" }],
]) {
  test(`${page} validation error follows locale changes after creation`, async () => {
    const harness = authHarness(page, values);
    await harness.submit();
    assert.equal(harness.renderError(), key);
    harness.setLocale("en");
    assert.equal(harness.renderError(), english[key]);
    harness.setLocale("zh-CN");
    assert.equal(harness.renderError(), key);
  });
}

test("setup server error stays verbatim even when it matches a translated UI key", async () => {
  const message = "密码长度至少 8 位";
  const harness = authHarness("setup", { password: "abcdefgh", confirm: "abcdefgh",
    api: { initPassword: async () => { throw new Error(message); } } });
  await harness.submit();
  assert.equal(harness.renderError(), message);
  harness.setLocale("en");
  assert.equal(harness.renderError(), message);
});


test("login startup failure follows locale hydration after the request failed", async () => {
  const key = "无法连接到后端服务";
  const harness = authHarness("login", { auth: { loadSession: async () => { throw new Error("backend unavailable"); } } }, "checkSession");
  await harness.submit();
  assert.equal(harness.renderError(), key);
  harness.setLocale("en");
  assert.equal(harness.renderError(), english[key]);
});
