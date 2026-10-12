import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import { runInNewContext } from "node:vm";

const require = createRequire(import.meta.url);
const React = require("react");
const { renderToStaticMarkup } = require("react-dom/server");
const compile = (source, fileName) =>
  ts.transpileModule(source, {
    fileName,
    compilerOptions: {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.CommonJS,
      jsx: ts.JsxEmit.ReactJSX,
      esModuleInterop: true,
    },
  }).outputText;
function evaluate(source, context = {}, fileName = "component.tsx") {
  const module = { exports: {} };
  runInNewContext(compile(source, fileName), { require, module, exports: module.exports, ...context });
  return module.exports;
}
const { syncOutcome } = evaluate(readFileSync(new URL("./sync-outcome.ts", import.meta.url), "utf8"));
const { translate } = evaluate(readFileSync(new URL("./i18n/index.tsx", import.meta.url), "utf8"), {
  require: (name) =>
    name === "./en.json" ? JSON.parse(readFileSync(new URL("./i18n/en.json", import.meta.url))) : require(name),
});

// Compile the actual page callback and result component. Only unrelated page
// loading/configuration is omitted; no sync decision logic is replaced.
const page = readFileSync(new URL("../app/(main)/function/sync/page.tsx", import.meta.url), "utf8");
const ast = ts.createSourceFile("page.tsx", page, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const workbench = ast.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "SyncWorkbench");
const runSync = workbench?.body?.statements.find(
  (node) =>
    ts.isVariableStatement(node) &&
    node.declarationList.declarations.some((item) => item.name.getText(ast) === "runSync"),
);
const resultComponent = ast.statements.find(
  (node) => ts.isFunctionDeclaration(node) && node.name?.text === "SyncResult",
);
assert.ok(runSync && resultComponent, "production sync callback and result component must exist");
const production = `${runSync.getText(ast)}\n${resultComponent.getText(ast)}\nmodule.exports = { runSync, SyncResult };`;

const base = { status: "success", truncated: false, synced: {}, companies: null, warnings: null, errors: null };
function harness(response, locale = "zh-CN") {
  const toasts = [];
  const states = [];
  const requests = [];
  const uiText = (text, params) => translate(locale, text, params);
  const { runSync, SyncResult } = evaluate(production, {
    require,
    syncOutcome,
    uiText,
    useI18n: () => ({ t: uiText }),
    Badge: ({ children }) => React.createElement("span", null, children),
    toast: Object.fromEntries(
      ["success", "warning", "error"].map((kind) => [kind, (message) => toasts.push({ kind, message })]),
    ),
    api: {
      ssSync: async (body) => {
        requests.push(body);
        if (response instanceof Error) throw response;
        return response;
      },
    },
    dimension: "project",
    selected: new Set(["raw-project-id"]),
    chosenTypes: ["subdomain", "service", "app"],
    createCompany: true,
    setSyncing: (value) => states.push(["syncing", value]),
    setResult: (value) => states.push(["result", value]),
  });
  return {
    runSync,
    toasts,
    states,
    requests,
    render: () => renderToStaticMarkup(React.createElement(SyncResult, { result: response })),
  };
}

for (const locale of ["zh-CN", "en"]) {
  for (const [description, response, kind, title] of [
    [
      "mixed writes and errors",
      { ...base, status: "partial", synced: { subdomain: 2, service: 3 }, errors: ["原始写入错误"] },
      "warning",
      "同步部分完成",
    ],
    ["all writes failed", { ...base, status: "failed", errors: ["原始来源错误"] }, "error", "同步失败"],
    [
      "warning only with zero writes",
      { ...base, status: "partial", warnings: ["原始来源警告"] },
      "warning",
      "同步部分完成",
    ],
    [
      "truncated writes",
      { ...base, status: "partial", truncated: true, synced: { app: 5000 } },
      "warning",
      "同步部分完成",
    ],
    ["missing legacy status", { ...base, status: undefined }, "warning", "同步结果未确认"],
    ["unknown status", { ...base, status: "done" }, "warning", "同步结果未确认"],
    ["missing completeness metadata", { ...base, truncated: undefined }, "warning", "同步结果未确认"],
    ["contradictory success with errors", { ...base, errors: ["原始错误"] }, "error", "同步失败"],
    ["contradictory success with warnings", { ...base, warnings: ["原始警告"] }, "warning", "同步部分完成"],
    ["contradictory success with truncation", { ...base, truncated: true }, "warning", "同步部分完成"],
  ]) {
    test(`${locale}: ${description} cannot report a successful completion`, async () => {
      const h = harness(response, locale);
      await h.runSync();
      assert.equal(h.toasts.length, 1);
      assert.equal(h.toasts[0].kind, kind);
      assert.ok(h.toasts[0].message.startsWith(translate(locale, title)));
      const markup = h.render();
      assert.ok(markup.includes(translate(locale, title)));
      assert.match(markup, /role="status"/);
      for (const raw of [...(response.warnings ?? []), ...(response.errors ?? [])]) assert.ok(markup.includes(raw));
      assert.equal(h.states.at(-1)[1], false);
      assert.equal(h.states.find(([key, value]) => key === "result" && value)[1], response);
    });
  }

  for (const count of [0, 1, 3]) {
    test(`${locale}: a complete sync reports ${count} successful writes with a valid count template`, async () => {
      const response = { ...base, synced: count === 0 ? null : { subdomain: count }, companies: ["原始企业名称"] };
      const h = harness(response, locale);
      await h.runSync();
      assert.equal(h.toasts[0].kind, "success");
      const countText = translate(locale, "成功写入的资产记录数：{v0}", { v0: count });
      assert.ok(h.toasts[0].message.includes(countText));
      assert.ok(h.render().includes(countText));
      assert.ok(h.render().includes("原始企业名称"));
      assert.deepEqual(JSON.parse(JSON.stringify(h.requests)), [
        {
          dimension: "project",
          targets: ["raw-project-id"],
          asset_types: ["subdomain", "service", "app"],
          create_company: true,
        },
      ]);
    });
  }

  test(`${locale}: truncation has an actionable persistent explanation`, () => {
    const markup = harness({ ...base, status: "partial", truncated: true }, locale).render();
    assert.ok(
      markup.includes(
        translate(locale, "部分目标的某些资产类型达到 5000 条上限，仍有记录未同步。请在数据源缩小目标范围后重新同步。"),
      ),
    );
    assert.ok(markup.includes(translate(locale, "计数包含新增和更新的成功写入，不代表新增或去重后的资产数。")));
  });
}

test("network failures use the error notification and release the busy state", async () => {
  const h = harness(new Error("controlled transport failure"), "en");
  await h.runSync();
  assert.deepEqual(h.toasts, [{ kind: "error", message: "Sync failed: controlled transport failure" }]);
  assert.equal(h.states.at(-1)[1], false);
});

test("the actual API client preserves partial and failed outcome metadata from JSON", async () => {
  const source = readFileSync(new URL("./api.ts", import.meta.url), "utf8");
  for (const response of [
    { ...base, status: "partial", truncated: true, synced: { app: 5000 }, warnings: ["原始截断警告"] },
    { ...base, status: "failed", errors: ["原始来源错误"] },
  ]) {
    const requests = [];
    const { api } = evaluate(
      source,
      {
        require: (name) => {
          if (name === "@/lib/auth") return { auth: { generation: () => 0 } };
          if (name === "@/lib/mock/enabled") return { MOCK: false };
          if (name === "@/lib/mock/handler") return { mockHandle: () => assert.fail("unexpected mock request") };
          assert.fail(`unexpected API import: ${name}`);
        },
        Headers,
        FormData,
        fetch: async (url, options) => {
          requests.push({ url, options });
          return new Response(JSON.stringify(response), { status: 200 });
        },
      },
      "api.ts",
    );
    const received = await api.ssSync({ dimension: "task", targets: ["原始任务名"], asset_types: ["app"] });
    assert.deepEqual(received, response);
    assert.equal(requests[0].url, "/api/sync/scopesentry/sync");
    assert.equal(requests[0].options.method, "POST");
    const h = harness(received, "en");
    await h.runSync();
    assert.equal(h.toasts[0].kind, response.status === "failed" ? "error" : "warning");
  }
});

test("the production mock route supplies explicit completeness fields for a normal empty sync", async () => {
  const source = readFileSync(new URL("./mock/handler.ts", import.meta.url), "utf8");
  const ast = ts.createSourceFile("handler.ts", source, ts.ScriptTarget.Latest, true);
  const route = ast.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "route");
  const sync = route?.body?.statements.find(
    (node) => ts.isIfStatement(node) && node.expression.getText(ast) === 'path === "/sync/scopesentry/sync"',
  );
  assert.ok(sync, "production mock sync route must exist");
  const { mockSync } = evaluate(`function mockSync(path) { ${sync.getText(ast)} } module.exports = { mockSync };`);
  const response = mockSync("/sync/scopesentry/sync");
  assert.deepEqual(JSON.parse(JSON.stringify(response)), base);
  const h = harness(response, "en");
  await h.runSync();
  assert.equal(h.toasts[0].kind, "success");
  assert.match(h.toasts[0].message, /Successful asset record writes: 0/);
});
