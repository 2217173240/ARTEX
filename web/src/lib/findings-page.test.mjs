import ts from "typescript";

const translateUI = (text, params = {}) => text.replace(/\{(\w+)\}/g, (match, key) => String(params[key] ?? match));

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

// Exercise the real page callbacks/effects without a browser or live API. The
// hook harness renders after state updates and runs only changed effect deps.
const source = readFileSync(
  process.env.FINDINGS_PAGE_SOURCE ?? new URL("../app/(main)/function/findings/page.tsx", import.meta.url),
  "utf8",
);
const compiled = ts.transpileModule(source, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

const sameDeps = (before, after) =>
  before && after && before.length === after.length && before.every((value, index) => Object.is(value, after[index]));

function pageHarness(view = "asset", { deferAssetTree = false, storage = new Map(), taskOptions = [] } = {}) {
  if (!storage.has("artex_finding_list_preferences"))
    storage.set("artex_finding_list_preferences", JSON.stringify({ view }));
  const hooks = [];
  const effects = [];
  const timers = new Map();
  const requests = [];
  const groupRequests = [];
  const treeRequests = [];
  const deletes = [];
  const statusUpdates = [];
  const exports = [];
  const toasts = [];
  let nextHook = 0;
  let nextTimer = 0;
  let dirty = true;
  let tree;
  let pagination;
  let childPagination;

  const React = {
    __esModule: true,
    Fragment: "Fragment",
    useState(initial) {
      const index = nextHook++;
      if (!hooks[index]) hooks[index] = { value: typeof initial === "function" ? initial() : initial };
      const set = (update) => {
        const value = typeof update === "function" ? update(hooks[index].value) : update;
        if (!Object.is(value, hooks[index].value)) {
          hooks[index].value = value;
          dirty = true;
        }
      };
      return [hooks[index].value, set];
    },
    useRef(initial) {
      const index = nextHook++;
      if (!hooks[index]) hooks[index] = { current: initial };
      return hooks[index];
    },
    useMemo(callback, deps) {
      const index = nextHook++;
      if (!sameDeps(hooks[index]?.deps, deps)) hooks[index] = { deps, value: callback() };
      return hooks[index].value;
    },
    useCallback(callback, deps) {
      return React.useMemo(() => callback, deps);
    },
    useEffect(callback, deps) {
      const index = nextHook++;
      if (sameDeps(hooks[index]?.deps, deps)) return;
      effects.push(() => {
        hooks[index]?.cleanup?.();
        hooks[index] = { deps, cleanup: callback() };
      });
    },
  };
  const stats = { total: 0, pending: 0, critical: 0, high: 0, medium: 0, low: 0, vulnclasses: [], tasks: taskOptions };
  const api = {
    activeFindingRetests: async () => [],
    findingStats: async () => stats,
    findingAssetTree(query) {
      if (!deferAssetTree) return Promise.resolve({ nodes: [], finding_total: 0 });
      const request = { query, ...deferred() };
      treeRequests.push(request);
      return request.promise;
    },
    findingGroups(query) {
      const request = { query, ...deferred() };
      groupRequests.push(request);
      return request.promise;
    },
    findingsPage(query) {
      const request = { query, ...deferred() };
      requests.push(request);
      return request.promise;
    },
    deleteFinding(id) {
      const request = { id, ...deferred() };
      deletes.push(request);
      return request.promise;
    },
    setFindingStatus(id, status) {
      const request = { id, status, ...deferred() };
      statusUpdates.push(request);
      return request.promise;
    },
    exportFindings: async (options) => {
      exports.push(options);
    },
  };
  const components = new Proxy(
    { __esModule: true, default: "DefaultComponent" },
    { get: (target, key) => target[key] ?? key },
  );
  const modules = {
    react: React,
    "react/jsx-runtime": { jsx: (type, props) => ({ type, props }), jsxs: (type, props) => ({ type, props }) },
    "@/lib/api": { api },
    "@/lib/i18n": {
      useI18n: () => ({
        t: translateUI,
      }),
    },
    "@/lib/local-storage.client": {
      getLocalStorageValue: (key) => storage.get(key),
      setLocalStorageValue: (key, value) => storage.set(key, value),
    },
    "@/lib/status": { statusMeta: () => ({ label: "status" }) },
    "@/lib/utils": { cn: (...values) => values.filter(Boolean).join(" ") },
    sonner: { toast: { error: (message) => toasts.push(message), success: () => undefined } },
    "./_components/asset-tree": { AssetTree: "AssetTree", assetPathOf: () => [] },
    "./_components/findings-table": {
      FindingsTable: "FindingsTable",
      FINDING_STATUSES: ["pending", "confirmed", "false_positive", "fixed"],
      SEVERITIES: ["critical", "high", "medium", "low"],
      UNASSIGNED_TASK: "__unassigned__",
      findingRowKey: (finding) => finding.finding_id,
      isSameFinding: (a, b) => a.finding_id === b.finding_id,
      fmtTime: String,
    },
  };
  const schedule = (callback, delay, interval = false) => {
    const id = ++nextTimer;
    timers.set(id, { callback, delay, interval });
    return id;
  };
  const module = { exports: {} };
  runInNewContext(compiled, {
    module,
    exports: module.exports,
    require: (name) => modules[name] ?? components,
    setInterval: (callback, delay) => schedule(callback, delay, true),
    clearInterval: (id) => timers.delete(id),
    setTimeout: schedule,
    clearTimeout: (id) => timers.delete(id),
    window: { setTimeout: schedule, clearTimeout: (id) => timers.delete(id) },
  });

  async function flush() {
    for (let turn = 0; turn < 20; turn++) {
      if (dirty) {
        dirty = false;
        nextHook = 0;
        tree = module.exports.default();
        pagination = elements(tree).findLast((node) => node.type === "TablePagination")?.props ?? pagination;
        childPagination =
          elements(tree).find((node) => node.type === "TablePagination" && !node.props.pageSizeOptions)?.props ??
          childPagination;
        for (const effect of effects.splice(0)) effect();
      }
      await Promise.resolve();
      if (!dirty && effects.length === 0) {
        await Promise.resolve();
        if (!dirty) return;
      }
    }
    assert.fail("page failed to settle");
  }

  function elements(node) {
    if (!node || typeof node !== "object") return [];
    if (Array.isArray(node)) return node.flatMap((child) => elements(child));
    return [node, ...elements(node.props?.children)];
  }
  function find(type, predicate = () => true) {
    const element = elements(tree).find((node) => node.type === type && predicate(node.props));
    assert.ok(element, `missing ${type}`);
    return element.props;
  }
  const text = (node) => {
    if (Array.isArray(node)) return node.map(text).join("");
    if (node && typeof node === "object") return text(node.props?.children);
    return node === null || node === undefined || node === false ? "" : String(node);
  };

  return {
    requests,
    groupRequests,
    treeRequests,
    deletes,
    statusUpdates,
    storage,
    exports,
    toasts,
    visibleText: () => text(tree),
    hasTable: () => elements(tree).some((node) => node.type === "FindingsTable"),
    paginationCount: () => elements(tree).filter((node) => node.type === "TablePagination").length,
    hasAssetTree: () => elements(tree).some((node) => node.type === "AssetTree"),
    assetKeys: () => Array.from(find("AssetTree").nodes, (node) => node.key),
    async retry(label) {
      find("Button", (props) =>
        label ? text(props.children).trim() === label : ["重新加载", "重试"].includes(text(props.children)),
      ).onClick();
      await flush();
    },
    flush,
    flat: () =>
      hooks.find(
        (hook) => hook?.value && typeof hook.value === "object" && "items" in hook.value && "loaded" in hook.value,
      )?.value,
    rows: () => Array.from(find("FindingsTable").items, (finding) => finding.finding_id),
    selected: () => [...find("FindingsTable").selectedIds],
    async bulkStatus(status) {
      find("Select", (props) => props.value === "").onValueChange(status);
      await flush();
    },
    async bulkDelete() {
      find("Button", (props) => text(props.children).trim() === "删除所选").onClick();
      await flush();
      find("Button", (props) => props.variant === "destructive" && text(props.children).trim() === "删除").onClick();
      await flush();
    },
    async changeTask(task) {
      find(
        "Select",
        (props) =>
          props.value === "all" &&
          elements({ props }).some((node) => node.type === "SelectItem" && node.props.value === "1"),
      ).onValueChange(task);
      await flush();
    },
    async selectAsset(scope) {
      find("AssetTree").onSelect(scope);
      await flush();
    },
    async changeView(next) {
      find("Tabs").onValueChange(next);
      await flush();
    },
    async changeSeverity(severity) {
      find("ToggleGroup").onValueChange(severity);
      await flush();
    },
    async expandGroup(id = "1") {
      find("button", (props) => "aria-expanded" in props && text(props.children).includes(`任务 #${id}`)).onClick();
      await flush();
    },
    async changeGroupPage(page) {
      assert.ok(childPagination, "missing child pagination");
      childPagination.onPageChange(page);
      await flush();
    },
    async changeGroupPageSize(size) {
      assert.ok(childPagination, "missing child pagination");
      childPagination.onPageSizeChange(size);
      await flush();
    },
    async resolveGroups(index, items = [], total = 100) {
      groupRequests[index].resolve({ items, total, finding_total: total });
      await flush();
    },
    async rejectGroups(index) {
      groupRequests[index].reject(new Error("controlled group failure"));
      await flush();
    },
    async resolveTree(index, nodes = [], total = 0) {
      treeRequests[index].resolve({ nodes, finding_total: total });
      await flush();
    },
    async rejectTree(index) {
      treeRequests[index].reject(new Error("controlled tree failure"));
      await flush();
    },
    async changePage(page) {
      pagination.onPageChange(page);
      await flush();
    },
    async changePageSize(size) {
      pagination.onPageSizeChange(size);
      await flush();
    },
    async resolve(index, items = [], total = 100) {
      const { query } = requests[index];
      requests[index].resolve({ items, total, page: query.page, page_size: query.pageSize });
      await flush();
    },
    async reject(index) {
      requests[index].reject(new Error("controlled failure"));
      await flush();
    },
    async poll(times = 1) {
      for (let count = 0; count < times; count++) {
        for (const timer of [...timers.values()]) {
          if (timer.interval && timer.delay === 5000) timer.callback();
        }
      }
      await flush();
    },
    deleteFinding(finding) {
      return find("FindingsTable").onDelete(finding);
    },
    async finishDelete(index = 0) {
      deletes[index].resolve({ deleted: 1 });
      await flush();
    },
    async exportScope(scope) {
      find("Button", (props) => props.size === "sm" && text(props.children).trim() === "导出").onClick();
      await flush();
      find("RadioGroup", (props) => ["filtered", "all", "selected"].includes(props.value)).onValueChange(scope);
      await flush();
      await find("Button", (props) => !props.size && text(props.children).trim() === "导出").onClick();
      await flush();
    },
    async selectFinding(id) {
      find("FindingsTable").onToggleSelected(id, true);
      await flush();
    },
  };
}

const finding = (id) => ({ finding_id: id, task_id: "1", status: "pending", severity: "high" });

test("asset navigation starts the new request while the previous asset is pending", async () => {
  const page = pageHarness();
  await page.flush();
  await page.resolve(0);
  await page.selectAsset("a:1");
  await page.selectAsset("a:2");
  assert.equal(page.requests.length, 3);
  assert.equal(page.requests[2].query.assetScope, "a:2");
  await page.resolve(2, [finding("B")]);
  await page.resolve(1, [finding("A")]);
  assert.deepEqual(page.rows(), ["B"]);
  assert.equal(page.flat().loading, false);
});

test("an old asset failure cannot clear the latest request's loading state", async () => {
  const page = pageHarness();
  await page.flush();
  await page.resolve(0);
  await page.selectAsset("a:1");
  await page.selectAsset("a:2");
  assert.equal(page.requests.length, 3);
  await page.reject(1);
  assert.equal(page.flat().loading, true);
  await page.resolve(2, [finding("B")]);
  assert.deepEqual(page.rows(), ["B"]);
});

test("a failed new asset query shows an error instead of the previous asset's snapshot", async () => {
  const page = pageHarness();
  await page.flush();
  await page.resolve(0, [finding("previous")]);
  await page.selectAsset("a:2");
  await page.reject(1);
  assert.equal(page.hasTable(), false);
  assert.match(page.visibleText(), /发现加载失败/);
  assert.equal(page.flat().items.length, 0);
  assert.equal(page.flat().total, 0);
  assert.equal(page.flat().loading, false);
});

test("an initial failure is distinct from an empty result and can be retried", async () => {
  const page = pageHarness("flat");
  await page.flush();
  await page.reject(0);
  assert.equal(page.hasTable(), false);
  assert.match(page.visibleText(), /发现加载失败/);
  await page.retry();
  assert.equal(page.requests.length, 2);
  assert.match(page.visibleText(), /正在加载发现/);
  await page.resolve(1, [finding("retried")], 1);
  assert.deepEqual(page.rows(), ["retried"]);
  assert.equal(page.flat().error, null);
});

test("a failed background refresh retains the last result and exposes a retry", async () => {
  const page = pageHarness("flat");
  await page.flush();
  await page.resolve(0, [finding("retained")], 1);
  await page.poll();
  await page.reject(1);
  assert.deepEqual(page.rows(), ["retained"]);
  assert.match(page.visibleText(), /更新失败，正在显示上次结果/);
  await page.retry();
  await page.resolve(2, [finding("fresh")], 1);
  assert.deepEqual(page.rows(), ["fresh"]);
  assert.doesNotMatch(page.visibleText(), /更新失败/);
});

test("pagination and page size changes replace pending requests immediately", async () => {
  const page = pageHarness("flat");
  await page.flush();
  await page.resolve(0, [finding("first")]);
  await page.changePage(2);
  await page.changePage(3);
  await page.changePageSize(50);
  assert.equal(page.requests.length, 4);
  assert.equal(page.requests[3].query.page, 1);
  assert.equal(page.requests[3].query.pageSize, 50);
  await page.reject(1);
  await page.resolve(2, [finding("old-page")]);
  assert.equal(page.flat().loading, true);
  await page.resolve(3, [finding("new-size")]);
  assert.deepEqual(page.rows(), ["new-size"]);
});

test("polls deduplicate an in-flight query and preserve its successful snapshot on failure", async () => {
  const page = pageHarness("flat");
  await page.flush();
  await page.poll(2);
  assert.equal(page.requests.length, 1);
  await page.resolve(0, [finding("saved")]);
  await page.poll(2);
  assert.equal(page.requests.length, 2);
  await page.reject(1);
  assert.deepEqual(page.rows(), ["saved"]);
  assert.equal(page.flat().loading, false);
});

test("deleting a finding supersedes an in-flight poll so it cannot restore the deleted row", async () => {
  const page = pageHarness("flat");
  await page.flush();
  const removed = finding("removed");
  await page.resolve(0, [removed]);
  await page.poll();
  const deletion = page.deleteFinding(removed);
  await page.finishDelete();
  await deletion;
  await page.flush();
  assert.equal(page.requests.length, 3);
  await page.resolve(1, [removed]);
  assert.equal(page.flat().loading, true);
  await page.resolve(2, [], 0);
  assert.deepEqual(page.rows(), []);
});

test("deletion finishing after asset navigation refreshes the currently selected asset", async () => {
  const page = pageHarness();
  await page.flush();
  const removed = finding("removed");
  await page.resolve(0, [removed]);
  const deletion = page.deleteFinding(removed);
  await page.selectAsset("a:2");
  await page.finishDelete();
  await deletion;
  await page.flush();
  assert.equal(page.requests.length, 3);
  assert.equal(page.requests[2].query.assetScope, "a:2");
  await page.reject(1);
  assert.equal(page.flat().loading, true);
  await page.resolve(2, [finding("B")]);
  assert.deepEqual(page.rows(), ["B"]);
});

test("an old asset deletion cannot decrement another asset's total or move its current page", async () => {
  const page = pageHarness();
  await page.flush();
  await page.resolve(0);
  await page.selectAsset("a:1");
  const removed = finding("A");
  await page.resolve(1, [removed], 1);
  const deletion = page.deleteFinding(removed);

  await page.selectAsset("a:2");
  await page.resolve(2, [finding("B-first")], 21);
  await page.changePage(2);
  await page.resolve(3, [finding("B-last")], 21);
  await page.finishDelete();
  await deletion;
  await page.flush();

  assert.equal(page.flat().total, 21);
  assert.equal(page.requests.length, 5);
  assert.equal(page.requests[4].query.assetScope, "a:2");
  assert.equal(page.requests[4].query.page, 2);
  await page.resolve(4, [finding("B-last")], 21);
  assert.deepEqual(page.rows(), ["B-last"]);
});

test("deleting the last row of the last page starts the corrected page while refresh is pending", async () => {
  const page = pageHarness();
  await page.flush();
  await page.resolve(0, [finding("first")], 21);
  await page.changePage(2);
  const removed = finding("last");
  await page.resolve(1, [removed], 21);
  const deletion = page.deleteFinding(removed);
  await page.finishDelete();
  await deletion;
  await page.flush();
  assert.equal(page.requests.length, 4);
  assert.equal(page.requests[3].query.page, 1);
  await page.resolve(3, [finding("first")], 20);
  await page.resolve(2, [], 20);
  assert.deepEqual(page.rows(), ["first"]);
});

test("filtered export uses the displayed asset scope while all and selected retain their scopes", async () => {
  const page = pageHarness();
  await page.flush();
  await page.resolve(0);
  await page.selectAsset("a:12");
  await page.resolve(1, [finding("scoped")], 1);
  await page.exportScope("filtered");
  assert.equal(page.exports[0].scope, "filtered");
  assert.equal(page.exports[0].filters.assetScope, "a:12");
  await page.exportScope("all");
  assert.equal(page.exports[1].scope, "all");
  assert.equal(page.exports[1].filters.assetScope, undefined);
  await page.selectFinding("scoped");
  await page.exportScope("selected");
  assert.equal(page.exports[2].scope, "selected");
  assert.deepEqual(Array.from(page.exports[2].ids), ["scoped"]);
  assert.equal(page.exports[2].filters.assetScope, undefined);
  await page.changeView("flat");
  await page.exportScope("filtered");
  assert.equal(page.exports[3].filters.assetScope, undefined);
});

const group = (id = "1") => ({
  task_id: id,
  task_name: `group-${id}`,
  count: 21,
  critical: 0,
  high: 21,
  medium: 0,
  low: 0,
});

test("initial group-list failure is retryable and distinct from no matches", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  assert.match(page.visibleText(), /正在加载任务分组/);
  assert.equal(page.paginationCount(), 0);
  assert.doesNotMatch(page.visibleText(), /没有匹配的发现/);
  await page.rejectGroups(0);
  assert.match(page.visibleText(), /任务分组加载失败/);
  assert.doesNotMatch(page.visibleText(), /没有匹配的发现/);
  assert.equal(page.paginationCount(), 0);
  await page.retry();
  await page.resolveGroups(1, [group()], 1);
  assert.match(page.visibleText(), /group-1/);
  assert.doesNotMatch(page.visibleText(), /加载失败/);
});

test("initial expanded-group failure is retryable without showing an empty table", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.resolveGroups(0, [group()], 1);
  await page.expandGroup();
  assert.match(page.visibleText(), /正在加载本组发现/);
  await page.reject(0);
  assert.equal(page.hasTable(), false);
  assert.match(page.visibleText(), /本组发现加载失败/);
  await page.retry();
  await page.resolve(1, [finding("retried-child")], 1);
  assert.deepEqual(page.rows(), ["retried-child"]);
});

test("group-list and child refresh failures retain snapshots and clear on retry", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.resolveGroups(0, [group()], 1);
  await page.expandGroup();
  await page.resolve(0, [finding("retained-child")], 21);
  await page.poll();
  await page.rejectGroups(1);
  await page.reject(1);
  assert.match(page.visibleText(), /任务分组更新失败，正在显示上次结果/);
  assert.match(page.visibleText(), /本组发现更新失败，正在显示上次结果/);
  assert.deepEqual(page.rows(), ["retained-child"]);
  await page.retry("重试任务分组");
  await page.resolveGroups(2, [group()], 1);
  await page.retry("重试本组发现");
  await page.resolve(2, [finding("fresh-child")], 21);
  assert.doesNotMatch(page.visibleText(), /更新失败/);
  assert.deepEqual(page.rows(), ["fresh-child"]);
});

test("group-list navigation ignores late page responses and never clamps from old totals", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.resolveGroups(0, [group("first")], 30);
  await page.changePage(2);
  await page.changePage(3);
  assert.equal(page.paginationCount(), 0);
  await page.resolveGroups(1, [group("stale-page")], 1);
  assert.doesNotMatch(page.visibleText(), /group-first|group-stale-page/);
  assert.equal(page.groupRequests.length, 3);
  await page.rejectGroups(2);
  assert.match(page.visibleText(), /任务分组加载失败/);
  await page.retry();
  assert.equal(page.groupRequests[3].query.page, 3);
  await page.resolveGroups(3, [group("latest")], 30);
  assert.match(page.visibleText(), /group-latest/);
});

test("late group filter requests cannot overwrite the new list or its loading state", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.changeSeverity("high");
  await page.rejectGroups(0);
  assert.match(page.visibleText(), /正在加载任务分组/);
  await page.poll(2);
  assert.equal(page.groupRequests.length, 2);
  await page.resolveGroups(1, [group("high")], 1);
  await page.poll();
  await page.changeSeverity("low");
  await page.resolveGroups(2, [group("stale-high")], 1);
  assert.doesNotMatch(page.visibleText(), /group-high|group-stale-high/);
  await page.resolveGroups(3, [group("low")], 1);
  assert.match(page.visibleText(), /group-low/);
});

test("child page failures hide previous pages and ignore late responses", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.resolveGroups(0, [group()], 1);
  await page.expandGroup();
  await page.resolve(0, [finding("first-child")], 30);
  await page.changeGroupPage(2);
  await page.changeGroupPage(3);
  assert.equal(page.paginationCount(), 1);
  await page.reject(1);
  assert.equal(page.hasTable(), false);
  assert.match(page.visibleText(), /正在加载本组发现/);
  await page.reject(2);
  assert.match(page.visibleText(), /本组发现加载失败/);
  assert.equal(page.requests.length, 3);
  await page.retry();
  assert.equal(page.requests[3].query.page, 3);
  await page.resolve(3, [finding("third-child")], 30);
  assert.deepEqual(page.rows(), ["third-child"]);
});

test("late child success cannot overwrite a newer page or clamp using stale totals", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.resolveGroups(0, [group()], 1);
  await page.expandGroup();
  await page.resolve(0, [finding("first-child")], 30);
  await page.changeGroupPage(2);
  await page.changeGroupPage(3);
  await page.resolve(2, [finding("third-child")], 30);
  await page.resolve(1, [finding("stale-child")], 1);
  assert.deepEqual(page.rows(), ["third-child"]);
  assert.equal(page.requests.length, 3);
});

test("late child filter responses cannot affect the newly expanded group", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.resolveGroups(0, [group()], 1);
  await page.expandGroup();
  await page.changeSeverity("low");
  await page.resolveGroups(1, [group()], 1);
  await page.expandGroup();
  await page.reject(0);
  assert.match(page.visibleText(), /正在加载本组发现/);
  await page.resolve(1, [finding("low-child")], 1);
  assert.deepEqual(page.rows(), ["low-child"]);
});

test("initial asset-tree failure exposes retry instead of an empty tree", async () => {
  const page = pageHarness("asset", { deferAssetTree: true });
  await page.flush();
  await page.resolve(0, [], 0);
  await page.rejectTree(0);
  assert.match(page.visibleText(), /资产树加载失败/);
  assert.equal(page.hasAssetTree(), false);
  await page.retry("重新加载资产树");
  await page.resolveTree(1);
  assert.doesNotMatch(page.visibleText(), /加载失败/);
});

test("a successful empty group list shows no matches only after loading", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  assert.doesNotMatch(page.visibleText(), /没有匹配的发现/);
  await page.resolveGroups(0, [], 0);
  assert.match(page.visibleText(), /没有匹配的发现/);
  assert.doesNotMatch(page.visibleText(), /正在加载|加载失败/);
});

test("group polling deduplicates slow list and child requests", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.poll(2);
  assert.equal(page.groupRequests.length, 1);
  await page.resolveGroups(0, [group()], 1);
  await page.expandGroup();
  await page.resolve(0, [finding("saved-child")], 30);
  await page.poll(2);
  assert.equal(page.groupRequests.length, 2);
  assert.equal(page.requests.length, 2);
  await page.rejectGroups(1);
  await page.reject(1);
  assert.deepEqual(page.rows(), ["saved-child"]);
});

test("a new group page-size query clears previous rows and ignores a late page failure", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.resolveGroups(0, [group()], 1);
  await page.expandGroup();
  await page.resolve(0, [finding("old-child")], 30);
  await page.changeGroupPage(2);
  await page.changeGroupPageSize(50);
  await page.reject(1);
  assert.equal(page.hasTable(), false);
  assert.match(page.visibleText(), /正在加载本组发现/);
  assert.equal(page.requests[2].query.page, 1);
  assert.equal(page.requests[2].query.pageSize, 50);
  await page.resolve(2, [finding("new-size-child")], 30);
  assert.deepEqual(page.rows(), ["new-size-child"]);
});

test("a child request finishing while another view is open settles its reusable group state", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.resolveGroups(0, [group()], 1);
  await page.expandGroup();
  await page.changeView("flat");
  await page.reject(0);
  await page.changeView("grouped");
  await page.resolveGroups(1, [group()], 1);
  assert.match(page.visibleText(), /本组发现加载失败/);
  assert.doesNotMatch(page.visibleText(), /正在加载本组发现/);
  await page.retry();
  await page.resolve(2, [finding("returned-child")], 1);
  assert.deepEqual(page.rows(), ["returned-child"]);
});

test("returning to an earlier filter cannot revive an obsolete child request", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.resolveGroups(0, [group()], 1);
  await page.expandGroup();
  await page.changeSeverity("low");
  await page.changeSeverity("all");
  await page.resolveGroups(2, [group()], 1);
  await page.resolve(0, [finding("obsolete-child")], 1);
  // The restored query keeps its expanded group and starts a fresh child request.
  assert.equal(page.hasTable(), false);
  assert.equal(page.requests.length, 2);
  await page.resolve(1, [finding("current-child")], 1);
  assert.deepEqual(page.rows(), ["current-child"]);
});

test("asset-tree refresh failure retains its tree and retry replaces it", async () => {
  const page = pageHarness("asset", { deferAssetTree: true });
  await page.flush();
  await page.resolve(0, [], 0);
  await page.resolveTree(0, [{ key: "retained-asset" }], 1);
  const removal = page.deleteFinding(finding("removed"));
  await page.finishDelete();
  await removal;
  await page.flush();
  await page.rejectTree(1);
  assert.match(page.visibleText(), /资产树更新失败，正在显示上次结果/);
  assert.deepEqual(page.assetKeys(), ["retained-asset"]);
  await page.retry("重试资产树");
  await page.resolveTree(2, [{ key: "fresh-asset" }], 1);
  assert.deepEqual(page.assetKeys(), ["fresh-asset"]);
});

test("old asset-tree requests cannot overwrite a new filter or clear its loading state", async () => {
  const page = pageHarness("asset", { deferAssetTree: true });
  await page.flush();
  await page.changeSeverity("low");
  await page.rejectTree(0);
  assert.match(page.visibleText(), /正在加载资产树/);
  assert.equal(page.hasAssetTree(), false);
  await page.resolveTree(1, [{ key: "low-asset" }], 1);
  assert.deepEqual(page.assetKeys(), ["low-asset"]);
});

test("a successful out-of-range group result loads the corrected page", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.resolveGroups(0, [group()], 30);
  await page.changePage(3);
  await page.resolveGroups(1, [], 10);
  assert.equal(page.groupRequests.length, 3);
  assert.equal(page.groupRequests[2].query.page, 1);
  assert.equal(page.paginationCount(), 0);
  await page.resolveGroups(2, [group("corrected")], 10);
  assert.match(page.visibleText(), /group-corrected/);
});

test("a successful out-of-range child result loads the corrected page", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.resolveGroups(0, [group()], 1);
  await page.expandGroup();
  await page.resolve(0, [finding("first-child")], 30);
  await page.changeGroupPage(3);
  await page.resolve(1, [], 10);
  assert.equal(page.requests.length, 3);
  assert.equal(page.requests[2].query.page, 1);
  assert.equal(page.hasTable(), false);
  await page.resolve(2, [finding("corrected-child")], 10);
  assert.deepEqual(page.rows(), ["corrected-child"]);
});

test("a deletion finishing during child navigation supersedes that pending page without clamping it", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.resolveGroups(0, [group()], 1);
  await page.expandGroup();
  const removed = finding("removed");
  await page.resolve(0, [removed], 30);
  const deletion = page.deleteFinding(removed);
  await page.changeGroupPage(2);
  await page.finishDelete();
  await deletion;
  await page.flush();
  assert.equal(page.requests.length, 3);
  assert.equal(page.requests[2].query.page, 2);
  await page.resolve(1, [removed], 30);
  assert.equal(page.hasTable(), false);
  assert.match(page.visibleText(), /正在加载本组发现/);
  await page.resolve(2, [finding("fresh-child")], 29);
  assert.deepEqual(page.rows(), ["fresh-child"]);
});

test("group-list page-size navigation resets to page one and rejects the old page snapshot", async () => {
  const page = pageHarness("grouped");
  await page.flush();
  await page.resolveGroups(0, [group("first")], 30);
  await page.changePage(3);
  await page.changePageSize(20);
  await page.resolveGroups(1, [group("stale-page")], 1);
  assert.doesNotMatch(page.visibleText(), /group-first|group-stale-page/);
  assert.equal(page.groupRequests.length, 3);
  assert.equal(page.groupRequests[2].query.page, 1);
  assert.equal(page.groupRequests[2].query.pageSize, 20);
  assert.equal(page.paginationCount(), 0);
  await page.resolveGroups(2, [group("new-size")], 30);
  assert.match(page.visibleText(), /group-new-size/);
});

test("selected bulk status preserves failed selection and reports individual failures", async () => {
  const page = pageHarness("flat");
  await page.flush();
  await page.resolve(0, [finding("1"), finding("2")], 2);
  await page.selectFinding("1");
  await page.selectFinding("2");
  await page.bulkStatus("fixed");
  assert.equal(page.statusUpdates[0].id, "1");
  page.statusUpdates[0].resolve({});
  await page.flush();
  assert.equal(page.statusUpdates[1].id, "2");
  page.statusUpdates[1].reject(new Error("permission denied"));
  await page.flush();
  assert.deepEqual(page.selected(), ["2"]);
  assert.match(page.visibleText(), /#2: permission denied/);
});

test("selected bulk delete completes other records after a failure", async () => {
  const page = pageHarness("flat");
  await page.flush();
  await page.resolve(0, [finding("1"), finding("2")], 2);
  await page.selectFinding("1");
  await page.selectFinding("2");
  await page.bulkDelete();
  page.deletes[0].reject(new Error("cannot delete"));
  await page.flush();
  assert.equal(page.deletes[1].id, "2");
  page.deletes[1].resolve({ deleted: true });
  await page.flush();
  assert.deepEqual(page.selected(), ["1"]);
  assert.match(page.visibleText(), /#1: cannot delete/);
});

test("task group expansion survives returning from a detail route under the same filter", async () => {
  const storage = new Map();
  const first = pageHarness("grouped", { storage });
  await first.flush();
  await first.resolveGroups(0, [group()], 1);
  await first.expandGroup();
  await first.resolve(0, [finding("1")], 1);
  const back = pageHarness("grouped", { storage });
  await back.flush();
  await back.resolveGroups(0, [group()], 1);
  assert.equal(back.requests.length, 1);
  await back.resolve(0, [finding("1")], 1);
  assert.deepEqual(back.rows(), ["1"]);
});

test("selected task autoopens once and a deliberate collapse persists on back", async () => {
  const storage = new Map([["artex_finding_list_preferences", JSON.stringify({ view: "grouped", task: "1" })]]);
  const first = pageHarness("grouped", { storage, taskOptions: [{ id: 1 }] });
  await first.flush();
  await first.resolveGroups(0, [group()], 1);
  assert.equal(first.requests.length, 1);
  await first.resolve(0, [finding("1")], 1);
  await first.expandGroup();
  assert.equal(first.hasTable(), false);
  const back = pageHarness("grouped", { storage, taskOptions: [{ id: 1 }] });
  await back.flush();
  await back.resolveGroups(0, [group()], 1);
  assert.equal(back.requests.length, 0);
  assert.equal(back.hasTable(), false);
});
