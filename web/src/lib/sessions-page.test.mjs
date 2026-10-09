import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

// Run the production declarations, list effects, and pagination callback in a
// small hook harness. AST selection avoids importing this tab's unrelated UI
// and transcript logic while keeping the asynchronous business logic intact.
const source = readFileSync(
  process.env.SESSIONS_PAGE_SOURCE ??
    new URL("../app/(main)/function/tasks/detail/_tabs/sessions-tab.tsx", import.meta.url),
  "utf8",
);
const ast = ts.createSourceFile("sessions-tab.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const component = ast.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "SessionsTab");
assert.ok(component?.body, "SessionsTab must exist");

function hasIdentifier(node, name) {
  if (ts.isIdentifier(node) && node.text === name) return true;
  return ts.forEachChild(node, (child) => hasIdentifier(child, name)) ?? false;
}

function declaredNames(node) {
  if (ts.isIdentifier(node)) return [node.text];
  if (ts.isArrayBindingPattern(node) || ts.isObjectBindingPattern(node)) {
    return node.elements.flatMap((element) => (ts.isBindingElement(element) ? declaredNames(element.name) : []));
  }
  return [];
}

const declarations = new Set([
  "intents",
  "intentAssets",
  "olderIntents",
  "firstIntentsHasMore",
  "olderIntentsHasMore",
  "hasLoadedOlderIntentsPage",
  "loadingOlderIntents",
  "olderIntentsRequestRef",
  "firstIntentsRef",
  "loadOlderIntents",
]);
const selected = component.body.statements.filter((node) => {
  if (ts.isVariableStatement(node)) {
    return node.declarationList.declarations.some((declaration) =>
      declaredNames(declaration.name).some((name) => declarations.has(name)),
    );
  }
  if (ts.isIfStatement(node)) return hasIdentifier(node, "olderIntentsRequestRef");
  if (!ts.isExpressionStatement(node) || !ts.isCallExpression(node.expression)) return false;
  const callee = node.expression.expression;
  return (
    ts.isPropertyAccessExpression(callee) &&
    ts.isIdentifier(callee.expression) &&
    callee.expression.text === "React" &&
    callee.name.text === "useEffect" &&
    (hasIdentifier(node, "firstIntentsRef") || hasIdentifier(node, "taskIntentAssets"))
  );
});
for (const name of declarations) {
  assert.ok(
    selected.some(
      (node) =>
        ts.isVariableStatement(node) &&
        node.declarationList.declarations.some((declaration) => declaredNames(declaration.name).includes(name)),
    ),
    `missing production declaration: ${name}`,
  );
}
assert.ok(selected.some((node) => ts.isExpressionStatement(node) && hasIdentifier(node, "firstIntentsRef")));
const compiled = ts.transpileModule(
  `function SessionsHarness({ taskId }) {
    ${selected.map((node) => node.getText(ast)).join("\n")}
    return { intents, olderIntents, firstIntentsHasMore, olderIntentsHasMore, hasLoadedOlderIntentsPage,
      loadingOlderIntents, loadOlderIntents };
  }
  module.exports = SessionsHarness;`,
  { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } },
).outputText;

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

function sessionsHarness(initialTaskId = "A") {
  const hooks = [];
  const effects = new Map();
  const timers = new Map();
  const requests = [];
  const writesAfterUnmount = [];
  let taskId = initialTaskId;
  let nextHook = 0;
  let nextTimer = 0;
  let dirty = true;
  let mounted = true;
  let view;

  const React = {
    useState(initial) {
      const index = nextHook++;
      if (!hooks[index]) hooks[index] = { value: typeof initial === "function" ? initial() : initial };
      const set = (update) => {
        if (!mounted) {
          writesAfterUnmount.push(index);
          return;
        }
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
    useCallback(callback, deps) {
      const index = nextHook++;
      if (!sameDeps(hooks[index]?.deps, deps)) hooks[index] = { deps, value: callback };
      return hooks[index].value;
    },
    useEffect(callback, deps) {
      const index = nextHook++;
      if (sameDeps(hooks[index]?.deps, deps)) {
        effects.delete(index);
        return;
      }
      effects.set(index, () => {
        hooks[index]?.cleanup?.();
        hooks[index] = { deps, cleanup: callback() };
      });
    },
  };
  const api = {
    taskIntentAssets: async () => [],
    intentsPage(requestTaskId, beforeID, limit) {
      const request = { taskId: requestTaskId, beforeID, limit, ...deferred() };
      requests.push(request);
      return request.promise;
    },
  };
  const module = { exports: {} };
  runInNewContext(compiled, {
    React,
    api,
    module,
    setInterval(callback, delay) {
      const id = ++nextTimer;
      timers.set(id, { callback, delay });
      return id;
    },
    clearInterval: (id) => timers.delete(id),
  });

  async function flush({ commitEffects = true } = {}) {
    for (let turn = 0; turn < 20; turn++) {
      if (mounted && dirty) {
        dirty = false;
        nextHook = 0;
        view = module.exports({ taskId });
      }
      if (mounted && commitEffects) {
        const pending = [...effects.values()];
        effects.clear();
        for (const effect of pending) effect();
      }
      await Promise.resolve();
      if (!dirty && (!commitEffects || effects.size === 0)) {
        // Settle then/catch/finally chains even when a stale callback makes no
        // state change. This also exposes every setter called after unmount.
        await Promise.resolve();
        await Promise.resolve();
        await Promise.resolve();
        if (!dirty) return;
      }
    }
    assert.fail("sessions page failed to settle");
  }

  return {
    requests,
    writesAfterUnmount,
    flush,
    state: () => view,
    olderIDs: () => Array.from(view.olderIntents, (intent) => intent.id),
    loadOlder: () => view.loadOlderIntents(),
    async changeTask(next, options) {
      taskId = next;
      dirty = true;
      await flush(options);
    },
    async resolve(index, items = [], hasMore = false, options) {
      requests[index].resolve({ items, hasMore });
      await flush(options);
    },
    async reject(index) {
      requests[index].reject(new Error("controlled failure"));
      await flush();
    },
    unmount() {
      mounted = false;
      dirty = false;
      effects.clear();
      for (const hook of hooks) hook?.cleanup?.();
    },
  };
}

const intent = (id) => ({ id: String(id) });

async function loadedTask() {
  const page = sessionsHarness();
  await page.flush();
  await page.resolve(0, [intent(100)], true);
  return page;
}

test("older pagination uses the oldest loaded ID and suppresses same-tick duplicate clicks", async () => {
  const page = await loadedTask();
  page.loadOlder();
  page.loadOlder();
  await page.flush();
  assert.equal(page.requests.length, 2);
  assert.equal(page.requests[1].taskId, "A");
  assert.equal(page.requests[1].beforeID, 100);
  assert.equal(page.requests[1].limit, 300);
  assert.equal(page.state().loadingOlderIntents, true);
  await page.resolve(1, [intent(100), intent(99)], true);
  assert.deepEqual(page.olderIDs(), ["99"]);
  assert.equal(page.state().hasLoadedOlderIntentsPage, true);
  assert.equal(page.state().olderIntentsHasMore, true);
  assert.equal(page.state().loadingOlderIntents, false);
  page.loadOlder();
  assert.equal(page.requests[2].beforeID, 99);
});

test("delayed older results from A do not appear after navigation to B", async () => {
  const page = await loadedTask();
  page.loadOlder();
  await page.flush();
  await page.changeTask("B");
  await page.resolve(2, [intent(200)], true);
  await page.resolve(1, [intent(99)], true);
  assert.deepEqual(page.olderIDs(), []);
  assert.equal(page.state().hasLoadedOlderIntentsPage, false);
  assert.equal(page.state().olderIntentsHasMore, false);
  assert.equal(page.state().loadingOlderIntents, false);
});

test("task navigation invalidates A before the replacement list effect runs", async () => {
  const page = await loadedTask();
  page.loadOlder();
  await page.flush();
  await page.changeTask("B", { commitEffects: false });
  await page.resolve(1, [intent(99)], true, { commitEffects: false });
  assert.deepEqual(page.olderIDs(), []);
  assert.equal(page.state().hasLoadedOlderIntentsPage, false);
  assert.equal(page.state().olderIntentsHasMore, false);
  await page.flush();
  assert.equal(page.requests[2].taskId, "B");
});

test("A to B to A does not revive the first A pagination request", async () => {
  const page = await loadedTask();
  page.loadOlder();
  await page.flush();
  await page.changeTask("B");
  await page.changeTask("A");
  await page.resolve(3, [intent(300)], true);
  page.loadOlder();
  await page.flush();
  assert.equal(page.requests.length, 5);
  await page.resolve(1, [intent(99)], true);
  assert.deepEqual(page.olderIDs(), []);
  assert.equal(page.state().loadingOlderIntents, true);
  await page.resolve(4, [intent(299)], false);
  assert.deepEqual(page.olderIDs(), ["299"]);
  assert.equal(page.state().loadingOlderIntents, false);
});

test("unmount prevents pending older results and finally from calling state setters", async () => {
  const page = await loadedTask();
  page.loadOlder();
  await page.flush();
  page.unmount();
  await page.resolve(1, [intent(99)], true);
  assert.deepEqual(page.writesAfterUnmount, []);
});

test("an old A finally cannot clear B's loading flag or unlock its pending request", async () => {
  const page = await loadedTask();
  page.loadOlder();
  await page.flush();
  await page.changeTask("B");
  await page.resolve(2, [intent(200)], true);
  page.loadOlder();
  await page.flush();
  assert.equal(page.requests.length, 4);
  await page.reject(1);
  assert.equal(page.state().loadingOlderIntents, true);
  page.loadOlder();
  assert.equal(page.requests.length, 4);
  await page.resolve(3, [intent(199)], false);
  assert.deepEqual(page.olderIDs(), ["199"]);
  assert.equal(page.state().loadingOlderIntents, false);
});

test("a rejected older page releases the lock so the same page can be retried", async () => {
  const page = await loadedTask();
  page.loadOlder();
  await page.flush();
  await page.reject(1);
  assert.deepEqual(page.olderIDs(), []);
  assert.equal(page.state().hasLoadedOlderIntentsPage, false);
  assert.equal(page.state().loadingOlderIntents, false);
  page.loadOlder();
  await page.flush();
  assert.equal(page.requests.length, 3);
  assert.equal(page.requests[2].beforeID, 100);
  await page.resolve(2, [intent(99)], false);
  assert.deepEqual(page.olderIDs(), ["99"]);
  assert.equal(page.state().hasLoadedOlderIntentsPage, true);
  assert.equal(page.state().loadingOlderIntents, false);
});
