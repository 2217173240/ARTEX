import { startVisibilityPoll } from "./visibility-poll.ts";
import assert from "node:assert/strict";
import test from "node:test";

function installMockDocument(initialHidden = false) {
  const prevDescriptor = Object.getOwnPropertyDescriptor(globalThis, "document");
  const listeners = new Map();
  let hidden = initialHidden;

  const mockDoc = {
    get hidden() {
      return hidden;
    },
    set hidden(value) {
      hidden = Boolean(value);
    },
    addEventListener(type, listener) {
      let set = listeners.get(type);
      if (!set) {
        set = new Set();
        listeners.set(type, set);
      }
      set.add(listener);
    },
    removeEventListener(type, listener) {
      listeners.get(type)?.delete(listener);
    },
    dispatch(type) {
      for (const listener of listeners.get(type) ?? []) {
        listener();
      }
    },
    listenerCount(type) {
      return listeners.get(type)?.size ?? 0;
    },
  };

  Object.defineProperty(globalThis, "document", {
    configurable: true,
    writable: true,
    value: mockDoc,
  });

  return {
    doc: mockDoc,
    restore() {
      if (prevDescriptor) {
        Object.defineProperty(globalThis, "document", prevDescriptor);
      } else {
        delete globalThis.document;
      }
    },
  };
}

test("skips scheduled ticks while document.hidden is true", (t) => {
  t.mock.timers.enable({ apis: ["setInterval"] });
  const env = installMockDocument(true);
  let calls = 0;

  const stop = startVisibilityPoll(
    () => {
      calls++;
    },
    1000,
    { immediate: false },
  );

  try {
    t.mock.timers.tick(3500);
    assert.equal(calls, 0);

    env.doc.hidden = false;
    t.mock.timers.tick(1000);
    assert.equal(calls, 1);
  } finally {
    stop();
    env.restore();
  }
});

test("returning from hidden to visible triggers one immediate guarded refresh", (t) => {
  t.mock.timers.enable({ apis: ["setInterval"] });
  const env = installMockDocument(false);
  let calls = 0;

  const stop = startVisibilityPoll(() => {
    calls++;
  }, 5000);

  try {
    assert.equal(calls, 1);

    env.doc.hidden = true;
    env.doc.dispatch("visibilitychange");
    assert.equal(calls, 1);

    t.mock.timers.tick(15000);
    assert.equal(calls, 1);

    env.doc.hidden = false;
    env.doc.dispatch("visibilitychange");
    assert.equal(calls, 2);
  } finally {
    stop();
    env.restore();
  }
});

test("slow async fn blocks overlapping ticks until it settles", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval"] });
  const env = installMockDocument(false);
  let calls = 0;
  let resolveCurrent = null;

  const stop = startVisibilityPoll(() => {
    calls++;
    return new Promise((resolve) => {
      resolveCurrent = resolve;
    });
  }, 1000);

  try {
    assert.equal(calls, 1);

    // Interval ticks while the first request is still pending must not overlap.
    t.mock.timers.tick(3000);
    assert.equal(calls, 1);

    // Visibility return while still in-flight must also be guarded.
    env.doc.dispatch("visibilitychange");
    assert.equal(calls, 1);

    // Settle the in-flight promise and flush its microtask queue.
    resolveCurrent?.();
    await Promise.resolve();
    await Promise.resolve();

    t.mock.timers.tick(1000);
    assert.equal(calls, 2);
  } finally {
    resolveCurrent?.();
    stop();
    env.restore();
  }
});

test("isPaused suppresses scheduled ticks and visibility refreshes", (t) => {
  t.mock.timers.enable({ apis: ["setInterval"] });
  const env = installMockDocument(true);
  let paused = true;
  let calls = 0;

  const stop = startVisibilityPoll(
    () => {
      calls++;
    },
    1000,
    { immediate: false, isPaused: () => paused },
  );

  try {
    env.doc.hidden = false;
    env.doc.dispatch("visibilitychange");
    t.mock.timers.tick(3000);
    assert.equal(calls, 0);

    paused = false;
    t.mock.timers.tick(1000);
    assert.equal(calls, 1);

    paused = true;
    t.mock.timers.tick(2000);
    assert.equal(calls, 1);
  } finally {
    stop();
    env.restore();
  }
});

test("cleanup removes visibility listener, stops timer, and ignores late resolutions", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval"] });
  const env = installMockDocument(false);
  let calls = 0;
  let resolveInFlight = null;

  const stop = startVisibilityPoll(() => {
    calls++;
    return new Promise((resolve) => {
      resolveInFlight = resolve;
    });
  }, 1000);

  try {
    assert.equal(calls, 1);
    assert.equal(env.doc.listenerCount("visibilitychange"), 1);

    stop();
    assert.equal(env.doc.listenerCount("visibilitychange"), 0);

    // Late resolution after cleanup must not crash or schedule extra work.
    resolveInFlight?.();
    await Promise.resolve();
    await Promise.resolve();

    env.doc.dispatch("visibilitychange");
    t.mock.timers.tick(5000);
    assert.equal(calls, 1);
  } finally {
    resolveInFlight?.();
    env.restore();
  }
});
