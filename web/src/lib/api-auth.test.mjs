import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

// Run the actual client with browser state and fetch controlled at its boundary.
const source = readFileSync(new URL("./api.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
}).outputText;

function apiHarness(response, browser = true) {
  const storage = new Map([
    ["artex_token", "expired-token"],
    ["theme", "dark"],
  ]);
  const cookies = new Map([
    ["artex_token", "expired-token"],
    ["theme", "dark"],
  ]);
  const cookieWrites = [];
  const requests = [];
  const downloads = [];
  const blobs = [];
  const revoked = [];
  const location = { href: "/function/tasks" };
  const document = {
    get cookie() {
      return Array.from(cookies, ([key, value]) => `${key}=${value}`).join("; ");
    },
    set cookie(value) {
      cookieWrites.push(value);
      const [pair] = value.split(";");
      const [key, token] = pair.split("=");
      if (/max-age=0/i.test(value)) cookies.delete(key);
      else cookies.set(key, token);
    },
    body: { appendChild: () => undefined },
    createElement(tag) {
      assert.equal(tag, "a");
      return {
        href: "",
        download: "",
        click() {
          downloads.push({ href: this.href, filename: this.download });
        },
        remove: () => undefined,
      };
    },
  };
  class BrowserURL extends URL {
    static createObjectURL(blob) {
      blobs.push(blob);
      return `blob:test-${blobs.length}`;
    }
    static revokeObjectURL(url) {
      revoked.push(url);
    }
  }
  const module = { exports: {} };
  const modules = {
    "@/lib/mock/enabled": { MOCK: false },
    "@/lib/mock/handler": { mockHandle: () => assert.fail("unexpected mock request") },
  };
  runInNewContext(compiled, {
    module,
    exports: module.exports,
    require(name) {
      assert.ok(name in modules, `unexpected import: ${name}`);
      return modules[name];
    },
    window: browser ? { location } : undefined,
    localStorage: {
      getItem: (key) => storage.get(key) ?? null,
      removeItem: (key) => storage.delete(key),
    },
    document,
    fetch: async (url, init) => {
      requests.push({ url, init });
      return typeof response === "function" ? response(url, init) : response;
    },
    Blob,
    File,
    FormData,
    Headers,
    URL: BrowserURL,
    URLSearchParams,
    setTimeout: (callback) => callback(),
  });
  return { ...module.exports, storage, cookies, cookieWrites, location, requests, downloads, blobs, revoked };
}

test("an expired workspace upload clears both login stores and redirects to login", async () => {
  const client = apiHarness(new Response('{"error":"expired token"}', { status: 401 }));
  await assert.rejects(client.api.workspaceUpload("uploads", [new File(["file"], "note.txt")]), (error) => {
    assert.ok(error instanceof client.ApiError);
    assert.equal(error.status, 401);
    return true;
  });
  assert.equal(client.storage.has("artex_token"), false);
  assert.equal(client.cookies.has("artex_token"), false);
  assert.equal(client.location.href, "/login");
  assert.equal(client.storage.get("theme"), "dark");
  assert.equal(client.cookies.get("theme"), "dark");
  assert.match(client.cookieWrites[0], /path=\/; max-age=0/);
  assert.equal(client.downloads.length, 0);
});

for (const [name, initialToken, currentToken, cleanup] of [
  ["unchanged login", "expired-token", "expired-token", true],
  ["newer login", "expired-token", "new-valid-token", false],
  ["cleared login", "expired-token", null, false],
  ["new login after an anonymous request", null, "new-valid-token", false],
  ["unchanged anonymous state", null, null, true],
]) {
  test(`a delayed write 401 respects ${name} and never replays the request`, async () => {
    let resolveResponse;
    const response = new Promise((resolve) => {
      resolveResponse = resolve;
    });
    const client = apiHarness(() => response);
    if (initialToken === null) client.storage.delete("artex_token");
    else client.storage.set("artex_token", initialToken);
    const pending = client.api.interceptSetToolConfig(["bash"]);
    if (currentToken === null) {
      client.storage.delete("artex_token");
      client.cookies.delete("artex_token");
    } else {
      client.storage.set("artex_token", currentToken);
      client.cookies.set("artex_token", currentToken);
    }
    resolveResponse(new Response(null, { status: 401 }));
    await assert.rejects(pending, (error) => {
      assert.ok(error instanceof client.ApiError);
      return error.status === 401 && error.message === "未授权";
    });
    assert.equal(client.requests.length, 1);
    assert.equal(
      new Headers(client.requests[0].init.headers).get("Authorization"),
      initialToken === null ? null : `Bearer ${initialToken}`,
    );
    assert.equal(client.storage.get("artex_token") ?? null, cleanup ? null : currentToken);
    assert.equal(client.cookies.get("artex_token") ?? null, cleanup ? null : currentToken);
    assert.equal(client.cookieWrites.length, cleanup ? 1 : 0);
    assert.equal(client.location.href, cleanup ? "/login" : "/function/tasks");
  });
}

for (const [authorization, cleanup] of [
  ["Bearer expired-token", true],
  ["Bearer old-token", false],
  ["Bearer different-token", false],
  ["Basic custom-credentials", false],
  ["", false],
]) {
  test(`a 401 with explicit Authorization ${JSON.stringify(authorization)} only clears its own session`, async () => {
    const client = apiHarness(new Response(null, { status: 401 }));
    await assert.rejects(
      client.http("/settings", { method: "POST", headers: { Authorization: authorization } }),
      (error) => {
        assert.ok(error instanceof client.ApiError);
        return error.status === 401 && error.message === "未授权";
      },
    );
    assert.equal(client.requests.length, 1);
    assert.equal(new Headers(client.requests[0].init.headers).get("Authorization"), authorization);
    assert.equal(client.storage.get("artex_token") ?? null, cleanup ? null : "expired-token");
    assert.equal(client.cookies.get("artex_token") ?? null, cleanup ? null : "expired-token");
    assert.equal(client.cookieWrites.length, cleanup ? 1 : 0);
    assert.equal(client.location.href, cleanup ? "/login" : "/function/tasks");
  });
}

test("a server-side 401 throws the typed error without browser cleanup", async () => {
  const client = apiHarness(new Response(null, { status: 401 }), false);
  await assert.rejects(client.http("/settings"), (error) => {
    assert.ok(error instanceof client.ApiError);
    return error.status === 401 && error.message === "未授权";
  });
  assert.equal(new Headers(client.requests[0].init.headers).has("Authorization"), false);
  assert.equal(client.storage.get("artex_token"), "expired-token");
  assert.equal(client.cookieWrites.length, 0);
  assert.equal(client.location.href, "/function/tasks");
});

const unauthorizedCalls = [
  ["chat upload", (api) => api.chatUpload("task", "task 1", [new File(["file"], "note.txt")])],
  ["skill upload", (api) => api.uploadSkill(new File(["zip"], "skill.zip"))],
  ["workspace download", (api) => api.workspaceDownload("uploads/note.txt")],
  ["finding export", (api) => api.exportFindings({ format: "csv", scope: "all" })],
  ["traffic body download", (api) => api.downloadFindingTrafficBody("finding-1", "binding-1", "response")],
  ["report", (api) => api.report("task-1")],
  ["read tool config", (api) => api.interceptGetToolConfig()],
  ["write tool config", (api) => api.interceptSetToolConfig(["bash"])],
];

for (const [name, invoke] of unauthorizedCalls) {
  test(`an expired ${name} clears both login stores and redirects before consuming success data`, async () => {
    const client = apiHarness(new Response('{"error":"expired token"}', { status: 401 }));
    let message;
    await assert.rejects(invoke(client.api), (error) => {
      assert.ok(error instanceof client.ApiError);
      assert.equal(error.status, 401);
      message = error.message;
      return true;
    });
    assert.equal(client.storage.has("artex_token"), false);
    assert.equal(client.cookies.has("artex_token"), false);
    assert.equal(client.location.href, "/login");
    assert.equal(message, "未授权");
    assert.equal(client.downloads.length, 0);
    assert.equal(client.blobs.length, 0);
  });
}

const uploads = [
  {
    name: "workspace",
    invoke: (api, files) => api.workspaceUpload("uploads/team one", files),
    url: "/api/workspace/upload?path=uploads%2Fteam%20one",
    result: { uploaded: 2 },
  },
  {
    name: "chat",
    invoke: (api, files) => api.chatUpload("staging", "session /1", files),
    url: "/api/chat/upload?scope=staging&id=session%20%2F1",
    result: { attachments: [{ name: "note.txt", path: "uploads/note.txt", size: 4 }] },
  },
  {
    name: "skill",
    invoke: (api, files) => api.uploadSkill(files[0], true),
    url: "/api/skills/upload?overwrite=true",
    result: { name: "example", files: 2 },
    single: true,
  },
];

for (const upload of uploads) {
  test(`${upload.name} upload sends FormData with the generated multipart boundary and returns JSON`, async () => {
    let encoded;
    const client = apiHarness(async (url, init) => {
      const request = new Request(`http://localhost${url}`, init);
      encoded = { contentType: request.headers.get("Content-Type"), body: await request.text() };
      return Response.json(upload.result);
    });
    const files = [new File(["file"], "note.txt"), new File(["archive"], "skill.zip")];
    assert.deepEqual(await upload.invoke(client.api, files), upload.result);
    const [{ url, init }] = client.requests;
    const headers = new Headers(init.headers);
    assert.equal(url, upload.url);
    assert.equal(init.method, "POST");
    assert.equal(headers.get("Authorization"), "Bearer expired-token");
    assert.equal(headers.has("Content-Type"), false);
    assert.ok(init.body instanceof FormData);
    assert.deepEqual(init.body.getAll("file"), upload.single ? files.slice(0, 1) : files);
    assert.match(encoded.contentType, /^multipart\/form-data; boundary=/);
    const boundary = encoded.contentType.split("boundary=")[1];
    assert.ok(encoded.body.startsWith(`--${boundary}\r\n`));
    assert.ok(encoded.body.endsWith(`--${boundary}--\r\n`));
    assert.ok(encoded.body.includes('name="file"; filename="note.txt"'));
    if (!upload.single) assert.ok(encoded.body.includes('name="file"; filename="skill.zip"'));
    assert.equal(client.storage.get("artex_token"), "expired-token");
    assert.equal(client.cookieWrites.length, 0);
    assert.equal(client.location.href, "/function/tasks");
  });
}

for (const [name, invoke] of [
  ["workspace upload", (client) => client.api.workspaceUpload("uploads", [])],
  ["chat upload", (client) => client.api.chatUpload("task", "task-1", [])],
  ["skill upload", (client) => client.api.uploadSkill(new File(["zip"], "skill.zip"))],
  ["blob download", (client) => client.api.workspaceDownload("note.txt")],
  ["JSON request", (client) => client.http("/settings")],
]) {
  test(`${name} retains the backend JSON error without clearing a valid login`, async () => {
    const client = apiHarness(Response.json({ error: "  缺少 SKILL.md\n" }, { status: 409 }));
    await assert.rejects(invoke(client), (error) => {
      assert.ok(error instanceof client.ApiError);
      assert.equal(error.status, 409);
      return error.message === "缺少 SKILL.md";
    });
    assert.equal(client.storage.get("artex_token"), "expired-token");
    assert.equal(client.cookies.get("artex_token"), "expired-token");
    assert.equal(client.cookieWrites.length, 0);
    assert.equal(client.location.href, "/function/tasks");
    assert.equal(client.downloads.length, 0);
  });
}

for (const body of ["not JSON", JSON.stringify({ error: "  " }), JSON.stringify({ error: 123 })]) {
  test(`an error without a usable JSON message uses the request status fallback: ${body}`, async () => {
    const client = apiHarness(new Response(body, { status: 500 }));
    await assert.rejects(client.api.workspaceUpload("uploads", []), (error) => {
      assert.ok(error instanceof client.ApiError);
      assert.equal(error.status, 500);
      return error.message === "POST /workspace/upload?path=uploads: 500";
    });
  });
}

for (const download of [
  {
    name: "workspace",
    invoke: (api) => api.workspaceDownload("folder/report.bin"),
    url: "/api/workspace/download?path=folder%2Freport.bin",
    filename: "report.bin",
  },
  {
    name: "finding export",
    invoke: (api) => api.exportFindings({ format: "csv", scope: "all" }),
    url: "/api/exploration/findings/export?format=csv&scope=all",
    filename: "findings.csv",
  },
  {
    name: "traffic evidence",
    invoke: (api) => api.downloadFindingTrafficBody("finding-1", "binding-1", "response", "task /1"),
    url: "/api/exploration/findings/finding-1/traffic/binding-1/body?side=response&download=1&context_task=task%20%2F1",
    filename: "evidence-binding-1-response.bin",
  },
]) {
  test(`successful ${download.name} download preserves binary data and its filename`, async () => {
    const bytes = Uint8Array.from([0, 1, 255, 10, 13]);
    const client = apiHarness(
      new Response(bytes, {
        headers: {
          "Content-Type": "application/octet-stream",
          "Content-Disposition": 'attachment; filename="findings.csv"',
        },
      }),
    );
    await download.invoke(client.api);
    assert.equal(client.requests[0].url, download.url);
    assert.equal(new Headers(client.requests[0].init.headers).get("Authorization"), "Bearer expired-token");
    assert.equal(client.blobs.length, 1);
    assert.ok(client.blobs[0] instanceof Blob);
    assert.equal(client.blobs[0].type, "application/octet-stream");
    assert.deepEqual(new Uint8Array(await client.blobs[0].arrayBuffer()), bytes);
    assert.deepEqual(client.downloads, [{ href: "blob:test-1", filename: download.filename }]);
    assert.deepEqual(client.revoked, ["blob:test-1"]);
    assert.equal(client.cookieWrites.length, 0);
  });
}

test("JSON requests retain headers, JSON content type, response parsing and 204 behavior", async () => {
  const client = apiHarness(Response.json({ ok: true }));
  assert.deepEqual(
    await client.http("/settings", {
      method: "POST",
      body: '{"mode":"active"}',
      headers: new Headers({ "X-Request-ID": "1" }),
    }),
    { ok: true },
  );
  const headers = new Headers(client.requests[0].init.headers);
  assert.equal(headers.get("Content-Type"), "application/json");
  assert.equal(headers.get("Authorization"), "Bearer expired-token");
  assert.equal(headers.get("X-Request-ID"), "1");
  const empty = apiHarness(new Response(null, { status: 204 }));
  assert.equal(await empty.http("/settings", { method: "DELETE" }), undefined);
});

test("a JSON 401 keeps the same login cleanup and unauthorized error contract", async () => {
  const client = apiHarness(new Response(null, { status: 401 }));
  await assert.rejects(client.http("/settings"), (error) => {
    assert.ok(error instanceof client.ApiError);
    assert.equal(error.status, 401);
    return error.message === "未授权";
  });
  assert.equal(client.storage.has("artex_token"), false);
  assert.equal(client.cookies.has("artex_token"), false);
  assert.equal(client.location.href, "/login");
});

test("report and tool config retain their text, JSON and void success responses", async () => {
  const report = apiHarness(new Response("# Report\n\n原始报告内容"));
  assert.equal(await report.api.report("task-1"), "# Report\n\n原始报告内容");
  const getConfig = apiHarness(Response.json({ enabled_tools: ["bash"] }));
  assert.deepEqual(await getConfig.api.interceptGetToolConfig(), { enabled_tools: ["bash"] });
  const setConfig = apiHarness(new Response(null));
  assert.equal(await setConfig.api.interceptSetToolConfig(["bash"]), undefined);
  assert.equal(setConfig.requests[0].init.method, "PUT");
  assert.equal(new Headers(setConfig.requests[0].init.headers).get("Content-Type"), "application/json");
  assert.deepEqual(JSON.parse(setConfig.requests[0].init.body), { enabled_tools: ["bash"] });
});
