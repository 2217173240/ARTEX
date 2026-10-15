import ts from "typescript";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { runInNewContext } from "node:vm";
const lib = path.dirname(fileURLToPath(import.meta.url));
function harness() {
  const cache = new Map(); let networkCalls = 0;
  function loadTS(filename) {
    if (cache.has(filename)) return cache.get(filename).exports;
    const module = { exports: {} }; cache.set(filename, module);
    const output = ts.transpileModule(readFileSync(filename, "utf8"), {
      compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
    }).outputText;
    runInNewContext(output, {
      module, exports: module.exports, URLSearchParams, structuredClone,
      setTimeout: callback => { callback(); return 1; },
      fetch: () => { networkCalls++; assert.fail("notification mock must not access the network"); },
      require: name => loadTS(path.resolve(path.dirname(filename), `${name}.ts`)),
    });
    return module.exports;
  }
  return { request: loadTS(path.join(lib, "mock/handler.ts")).mockHandle,
    fixtures: loadTS(path.join(lib, "mock/data.ts")), networkCalls: () => networkCalls };
}
const plain = value => JSON.parse(JSON.stringify(value));
test("notification mock supplies real page contracts and deletes only its runtime fixture", async () => {
  const h = harness(); const meta = await h.request("GET", "/notify/meta");
  assert.ok(Array.isArray(meta.kinds) && meta.kinds.length > 0);
  assert.equal(typeof meta.enabled, "boolean"); assert.equal(typeof meta.public_base_url, "string");
  assert.equal(typeof meta.digest_interval_min, "string"); assert.equal(typeof meta.defaults.digest_interval_min, "number");
  for (const key of ["channels", "channels_on", "pending", "failed", "sent_today", "backlog_age_ms"]) assert.equal(typeof meta.stats[key], "number");
  const { channels } = await h.request("GET", "/notify/channels");
  assert.ok(channels.length > 0); assert.equal(meta.stats.channels, channels.length);
  const channel = channels[0];
  assert.ok(meta.kinds.find(kind => kind.kind === channel.kind));
  assert.match(channel.name, /synthetic/i); assert.equal(channel.enabled, false);
  assert.match(channel.config.url, /\.invalid\//); assert.deepEqual(plain(channel.secret_keys), []);
  assert.equal(typeof channel.filter, "object"); assert.ok(Number.isSafeInteger(channel.id));
  channels[0].name = "consumer edit";
  assert.notEqual((await h.request("GET", "/notify/channels")).channels[0].name, "consumer edit");
  assert.deepEqual(plain(await h.request("GET", "/notify/deliveries?page=2&page_size=10&state=failed")), { deliveries: [], total: 0, page: 2, page_size: 10 });
  assert.deepEqual(plain(await h.request("DELETE", `/notify/channels/${channel.id}`)), { ok: true });
  assert.ok(!(await h.request("GET", "/notify/channels")).channels.some(item => item.id === channel.id));
  assert.equal((await h.request("GET", "/notify/meta")).stats.channels, meta.stats.channels - 1);
  assert.equal(h.fixtures.notificationChannels[0].id, channel.id);
  assert.match(h.fixtures.notificationChannels[0].name, /synthetic/i);
  assert.equal((await harness().request("GET", "/notify/channels")).channels.length, meta.stats.channels);
  assert.equal(h.networkCalls(), 0);
});
test("unsupported notification sends and writes fail explicitly without network", async () => {
  const h = harness();
  for (const [method, endpoint] of [["POST", "/notify/channels/9001/test"], ["POST", "/notify/deliveries/1/retry"], ["POST", "/notify/channels"], ["PATCH", "/notify/channels/9001"]]) {
    await assert.rejects(h.request(method, endpoint, "{}"), /reading and deleting demo channels only/);
  }
  await assert.rejects(h.request("DELETE", "/notify/channels/999999"), /not found/);
  const page = await h.request("GET", "/notify/deliveries?page=-1&page_size=garbage");
  assert.equal(page.page, 1); assert.equal(page.page_size, 50); assert.equal(h.networkCalls(), 0);
});
