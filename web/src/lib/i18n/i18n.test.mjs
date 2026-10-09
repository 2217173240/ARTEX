import assert from "node:assert/strict";
import fs from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import vm from "node:vm";

const require = createRequire(import.meta.url);
const ts = require("typescript");
const source = fs.readFileSync(new URL("./index.tsx", import.meta.url), "utf8");
const output = ts.transpileModule(source, { compilerOptions: {
  module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true,
} }).outputText;
const module = { exports: {} };
vm.runInNewContext(output, { require, module, exports: module.exports });
const { translate, I18nProvider, useI18n } = module.exports;

test("Chinese default, English labels, parameter substitution, and unknown content", () => {
  assert.equal(translate("zh-CN", "上传文件"), "上传文件");
  assert.equal(translate("en", "上传文件"), "Upload file");
  assert.equal(translate("en", "Target {name}", { name: "测试.example" }), "Target 测试.example");
  assert.equal(translate("en", "任意用户输入 / 原始证据"), "任意用户输入 / 原始证据");
});

test("server render uses stable Chinese default and preserves evidence", () => {
  const React = require("react");
  const { renderToString } = require("react-dom/server");
  function Probe() {
    const { locale, t } = useI18n();
    return React.createElement("div", { lang: locale }, t("上传文件"), React.createElement("pre", null, "上传文件"));
  }
  const rendered = renderToString(React.createElement(I18nProvider, null, React.createElement(Probe)));
  assert.match(rendered, /lang="zh-CN"/);
  assert.match(rendered, /<pre>上传文件<\/pre>/);
  assert.doesNotMatch(source, /TreeWalker|MutationObserver/);
});

test("preference hydration, toggling, document language, and blocked storage", () => {
  const React = require("react");
  let state = "zh-CN";
  let effects = [];
  let stored = '{"state":{"locale":"en"}}';
  let blocked = false;
  const document = { documentElement: { lang: "zh-CN" } };
  const localStorage = {
    getItem: () => { if (blocked) throw Error("denied"); return stored; },
    setItem: (_key, value) => { if (blocked) throw Error("denied"); stored = value; },
  };
  const hookReact = { ...React,
    useState: () => [state, (value) => { state = value; }],
    useEffect: (effect) => { effects.push(effect); },
    useCallback: (callback) => callback,
    useMemo: (factory) => factory(),
  };
  const sandboxModule = { exports: {} };
  vm.runInNewContext(output, { require: (id) => id === "react" ? hookReact : require(id),
    module: sandboxModule, exports: sandboxModule.exports, localStorage, document });
  const render = () => { effects = []; return sandboxModule.exports.I18nProvider({ children: null }).props.value; };
  assert.equal(render().locale, "zh-CN");
  effects.forEach((effect) => effect());
  const hydrated = render();
  assert.equal(hydrated.locale, "en");
  effects.forEach((effect) => effect());
  assert.equal(document.documentElement.lang, "en");
  hydrated.setLocale("zh-CN");
  assert.equal(stored, "zh-CN");
  blocked = true;
  assert.doesNotThrow(() => render().setLocale("en"));
  assert.equal(state, "en");
  assert.doesNotThrow(() => effects.forEach((effect) => effect()));
});

test("language selector exposes translated accessible label and switches locale", () => {
  const toggleSource = fs.readFileSync(new URL("../../components/language-toggle.tsx", import.meta.url), "utf8");
  const toggleOutput = ts.transpileModule(toggleSource, { compilerOptions: {
    module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true,
  } }).outputText;
  let locale = "en";
  const toggleModule = { exports: {} };
  vm.runInNewContext(toggleOutput, {
    require: (id) => id === "@/lib/i18n" ? { useI18n: () => ({ locale,
      t: (text) => translate(locale, text), setLocale: (next) => { locale = next; } }) } : require(id),
    module: toggleModule, exports: toggleModule.exports,
  });
  const selector = toggleModule.exports.LanguageToggle();
  assert.equal(selector.type, "select");
  assert.equal(selector.props["aria-label"], "Interface language");
  assert.equal(selector.props.title, "Interface language");
  assert.equal(selector.props.value, "en");
  selector.props.onChange({ target: { value: "zh-CN" } });
  assert.equal(toggleModule.exports.LanguageToggle().props.value, "zh-CN");
  assert.equal(toggleModule.exports.LanguageToggle().props["aria-label"], "界面语言");
});
