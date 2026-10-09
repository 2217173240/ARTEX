import { clsx } from "clsx";
import { twMerge } from "tailwind-merge";
import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import { runInNewContext } from "node:vm";

// Render the production JSX and its actual table/button/select wrappers. Only
// external primitives and unrelated UI are replaced, so these checks exercise
// the resulting element tree and callbacks rather than matching source text.
const require = createRequire(import.meta.url);
const jsx = (type, props) => ({ type, props });
const components = new Proxy({ __esModule: true }, { get: (target, key) => target[key] ?? key });
const icons = new Proxy(
  { __esModule: true },
  { get: (target, key) => target[key] ?? ((props) => jsx("svg", { "data-icon-name": key, ...props })) },
);
let nextTableId = 0;

function loadSource(path, modules) {
  const source = readFileSync(new URL(path, import.meta.url), "utf8");
  const compiled = ts.transpileModule(source, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
  }).outputText;
  const module = { exports: {} };
  runInNewContext(compiled, {
    module,
    exports: module.exports,
    require: (name) => modules[name] ?? components,
  });
  return module.exports;
}

function elements(node) {
  if (Array.isArray(node)) return node.flatMap(elements);
  if (!node || typeof node !== "object") return [];
  return [node, ...elements(node.props?.children)];
}

function renderElement(node) {
  if (Array.isArray(node)) return node.map(renderElement);
  if (!node || typeof node !== "object") return node;
  if (typeof node.type === "function") return renderElement(node.type(node.props));
  return { ...node, props: { ...node.props, children: renderElement(node.props?.children) } };
}

function classNames(node) {
  return new Set(node.props.className?.split(" ") ?? []);
}

function pathTo(node, predicate, parents = []) {
  if (Array.isArray(node)) {
    for (const child of node) {
      const path = pathTo(child, predicate, parents);
      if (path) return path;
    }
    return undefined;
  }
  if (!node || typeof node !== "object") return undefined;
  const path = [...parents, node];
  if (predicate(node)) return path;
  return pathTo(node.props?.children, predicate, path);
}

function click(tree, predicate) {
  const path = pathTo(tree, predicate);
  assert.ok(path, "missing click target");
  let stopped = false;
  const event = {
    stopPropagation: () => {
      stopped = true;
    },
  };
  for (const node of path.toReversed()) {
    node.props.onClick?.(event);
    if (stopped) break;
  }
}

const finding = (overrides = {}) => ({
  id: "node-1",
  finding_id: "finding-1",
  name: "跨越多个服务接口的认证绕过与敏感用户信息泄露漏洞",
  vulnclass: "Authentication bypass",
  summary: "summary",
  evidence: "request evidence",
  severity: "high",
  status: "pending",
  task_id: "task-1",
  task_description: "Task",
  ts: "2026-10-08T00:00:00Z",
  ...overrides,
});

function tableHarness(items = [finding()]) {
  const ids = [];
  let nextHook = 0;
  let tree;
  const toggles = [];
  const selections = [];
  const statuses = [];
  const React = {
    __esModule: true,
    Fragment: "Fragment",
    useId() {
      const index = nextHook++;
      ids[index] ??= `_table_${++nextTableId}_`;
      return ids[index];
    },
  };
  const modules = {
    react: React,
    "react/jsx-runtime": { jsx, jsxs: jsx },
    "@/lib/utils": { cn: (...inputs) => twMerge(clsx(inputs)) },
    "@/lib/status": { statusMeta: () => ({ label: "status" }) },
    "next/link": { __esModule: true, default: (props) => jsx("a", props) },
    "lucide-react": icons,
    "class-variance-authority": require("class-variance-authority"),
    "radix-ui": {
      Slot: { Root: "Slot" },
      Select: new Proxy({}, { get: (_target, key) => `Select${key}` }),
    },
  };
  modules["@/components/ui/table"] = loadSource("../components/ui/table.tsx", modules);
  modules["@/components/ui/button"] = loadSource("../components/ui/button.tsx", modules);
  modules["@/components/ui/select"] = loadSource("../components/ui/select.tsx", modules);
  const { FindingsTable, findingRowKey } = loadSource(
    "../app/(main)/function/findings/_components/findings-table.tsx",
    modules,
  );
  const props = {
    items,
    selectedIds: new Set(),
    onToggleSelected: (...args) => selections.push(args),
    onToggleSelectedPage: () => undefined,
    expandedKey: null,
    onToggleRow: (item) => toggles.push(item),
    reports: {},
    edit: null,
    onEditChange: () => undefined,
    saving: false,
    onSave: () => undefined,
    onStatusChange: (...args) => statuses.push(args),
    onRetest: () => undefined,
    activeRetests: {},
    onDeepen: () => undefined,
    onDelete: () => undefined,
  };
  return {
    toggles,
    selections,
    statuses,
    findingRowKey,
    render(overrides = {}) {
      nextHook = 0;
      tree = renderElement(FindingsTable({ ...props, ...overrides }));
      return tree;
    },
    find(predicate) {
      const node = elements(tree).find(predicate);
      assert.ok(node, "missing element");
      return node;
    },
  };
}

const isExpand = (node) => node.type === "button" && node.props["aria-controls"];

test("empty results explain the state without an unusable wide table", () => {
  const tree = tableHarness([]).render();
  assert.equal(tree.props.role, "status");
  const text = (node) =>
    Array.isArray(node)
      ? node.map(text).join("")
      : node && typeof node === "object"
        ? text(node.props?.children)
        : typeof node === "string"
          ? node
          : "";
  assert.match(text(tree), /当前条件下暂无发现/);
  assert.match(text(tree), /调整关键词/);
  assert.equal(
    elements(tree).some((node) => node.type === "table"),
    false,
  );
});

test("native rows contain a named disclosure button with a stable, existing details target", () => {
  const table = tableHarness();
  let tree = table.render();
  const rows = elements(tree).filter((node) => node.type === "tr");
  for (const row of rows) {
    assert.equal(row.props.role, undefined);
    assert.equal(row.props.tabIndex, undefined);
    assert.equal(row.props.onKeyDown, undefined);
    assert.equal(row.props["aria-expanded"], undefined);
  }
  const button = table.find(isExpand);
  assert.equal(button.props.type, "button");
  assert.equal(button.props["aria-expanded"], false);
  assert.equal(button.props["aria-label"], `展开漏洞详情：${finding().name}`);
  const targetId = button.props["aria-controls"];
  const collapsed = table.find((node) => node.props.id === targetId);
  assert.equal(collapsed.type, "tr");
  assert.equal(collapsed.props.hidden, true);
  assert.equal(
    elements(collapsed).some((node) => node.type === "pre"),
    false,
  );

  tree = table.render({ expandedKey: table.findingRowKey(finding()) });
  const expanded = table.find((node) => node.props.id === targetId);
  assert.equal(expanded.props.hidden, false);
  assert.equal(table.find(isExpand).props["aria-expanded"], true);
  assert.equal(table.find(isExpand).props["aria-label"], `收起漏洞详情：${finding().name}`);
  assert.equal(elements(expanded).find((node) => node.type === "td").props.colSpan, 9);
  assert.ok(elements(expanded).some((node) => node.type === "pre"));
  const findingIcons = new Set([
    "ChevronRightIcon",
    "ArrowUpRightIcon",
    "RotateCcwIcon",
    "FlaskConicalIcon",
    "Trash2Icon",
    "ShieldAlertIcon",
    "FileTextIcon",
  ]);
  assert.ok(
    elements(tree)
      .filter((node) => findingIcons.has(node.props["data-icon-name"]))
      .every((node) => node.props["aria-hidden"] === "true"),
  );
});

test("details ids are unique between table instances and task-local legacy findings", () => {
  const items = [finding({ finding_id: undefined }), finding({ finding_id: undefined, task_id: "task-2" })];
  const left = tableHarness(items);
  const right = tableHarness(items);
  const ids = [...elements(left.render()), ...elements(right.render())]
    .filter(isExpand)
    .map((node) => node.props["aria-controls"]);
  assert.equal(ids.length, 4);
  assert.equal(new Set(ids).size, 4);
});

test("disclosure, selection, links, and status updates retain independent actions", () => {
  const item = finding();
  const table = tableHarness([item]);
  const tree = table.render();
  click(tree, isExpand);
  assert.deepEqual(table.toggles, [item]);
  click(tree, (node) => node.type === "a" && node.props.href.startsWith("/function/findings/detail"));
  click(tree, (node) => node.type === "Checkbox" && node.props["aria-label"] === `选择漏洞：${item.name}`);
  click(tree, (node) => node.props["data-slot"] === "select-trigger");
  assert.equal(table.toggles.length, 1);

  const checkbox = table.find(
    (node) => node.type === "Checkbox" && node.props["aria-label"] === `选择漏洞：${item.name}`,
  );
  checkbox.props.onCheckedChange(true);
  assert.deepEqual(table.selections, [[item.finding_id, true]]);
  const status = table.find((node) => node.type === "SelectRoot" && node.props.value === "pending");
  status.props.onValueChange("confirmed");
  assert.deepEqual(table.statuses, [[item, "confirmed"]]);
  const trigger = table.find((node) => node.props["data-slot"] === "select-trigger");
  assert.equal(trigger.props["aria-label"], `更新漏洞状态：${item.name}`);
  assert.ok(classNames(trigger).has("focus-visible:ring-3"));
  assert.equal(classNames(trigger).has("focus-visible:ring-0"), false);
  const row = table.find((node) => node.type === "tr" && node.props.onClick);
  row.props.onClick();
  assert.equal(table.toggles.length, 2);
});

test("the named keyboard scroll region preserves table semantics and readable title layout", () => {
  const table = tableHarness();
  const tree = table.render();
  const scroll = table.find((node) => node.props["data-slot"] === "table-container");
  assert.equal(scroll.type, "div");
  assert.equal(scroll.props.role, "region");
  assert.equal(scroll.props.tabIndex, 0);
  assert.equal(scroll.props["aria-label"], "漏洞发现列表，可横向滚动");
  assert.ok(classNames(scroll).has("overflow-x-auto"));
  assert.ok(classNames(scroll).has("focus-visible:ring-2"));
  assert.equal(elements(tree).filter((node) => classNames(node).has("overflow-x-auto")).length, 1);
  const nativeTable = table.find((node) => node.type === "table");
  assert.equal(nativeTable.props.role, undefined);
  assert.equal(nativeTable.props.containerProps, undefined);
  assert.ok(classNames(nativeTable).has("min-w-[68rem]"));
  const titleHeader = table.find((node) => node.type === "th" && node.props.children === "漏洞名称");
  assert.ok(classNames(titleHeader).has("w-64"));
  const titlePath = pathTo(tree, (node) => node.type === "a" && node.props.children === finding().name);
  const title = titlePath.at(-1);
  const cell = titlePath.findLast((node) => node.type === "td");
  assert.ok(classNames(title).has("line-clamp-2"));
  assert.ok(classNames(title).has("break-words"));
  assert.equal(classNames(title).has("truncate"), false);
  assert.ok(classNames(cell).has("whitespace-normal"));
  assert.equal(classNames(cell).has("whitespace-nowrap"), false);
  const chevron = table.find((node) => node.props["data-icon-name"] === "ChevronRightIcon");
  assert.ok(classNames(chevron).has("motion-reduce:transition-none"));
});
