import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

function harness() {
  const source = readFileSync(
    process.env.THEME_UTILS_SOURCE ?? new URL("./preferences/theme-utils.ts", import.meta.url),
    "utf8",
  );
  const frames = [];
  const classes = new Set();
  const attributes = {};
  const root = {
    style: {},
    setAttribute: (key, value) => {
      attributes[key] = value;
    },
    classList: {
      add: (value) => classes.add(value),
      remove: (value) => classes.delete(value),
      toggle: (value, enabled) => (enabled ? classes.add(value) : classes.delete(value)),
    },
  };
  const module = { exports: {} };
  runInNewContext(
    ts.transpileModule(source, {
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
    }).outputText,
    {
      exports: module.exports,
      document: { documentElement: root },
      window: { matchMedia: () => ({ matches: true }) },
      requestAnimationFrame: (callback) => frames.push(callback),
    },
  );
  return {
    apply: module.exports.applyThemeMode,
    classes,
    attributes,
    root,
    frame: () => {
      for (const callback of frames.splice(0)) callback();
    },
  };
}

test("theme updates immediately and suppresses transitions through its first painted frame", () => {
  const page = harness();
  assert.equal(page.apply("dark"), "dark");
  assert.equal(page.attributes["data-theme-mode"], "dark");
  assert.equal(page.root.style.colorScheme, "dark");
  assert.ok(page.classes.has("dark"));
  assert.ok(page.classes.has("disable-transitions"));
  page.frame();
  assert.ok(page.classes.has("disable-transitions"));
  page.frame();
  assert.equal(page.classes.has("disable-transitions"), false);
});

test("an older transition cleanup cannot uncover a newer theme before it paints", () => {
  const page = harness();
  page.apply("dark");
  page.frame();
  page.apply("light");
  page.frame();
  assert.equal(page.classes.has("dark"), false);
  assert.ok(page.classes.has("disable-transitions"));
  page.frame();
  assert.equal(page.classes.has("disable-transitions"), false);
  assert.equal(page.apply("system"), "dark");
  assert.equal(page.attributes["data-theme-mode"], "system");
});
