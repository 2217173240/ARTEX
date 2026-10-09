import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

// Select callbacks from their owning components with the TypeScript AST, then
// execute the actual source with controlled API completion and view handoff.
const source = readFileSync(new URL("../app/(main)/chat/page.tsx", import.meta.url), "utf8");
const page = ts.createSourceFile("page.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);

function callbackSource(componentName) {
  const component = page.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === componentName);
  assert.ok(component?.body, `missing ${componentName}`);
  const callback = component.body.statements.find(
    (node) => ts.isFunctionDeclaration(node) && node.name?.text === "pickFiles",
  );
  assert.ok(callback, `missing ${componentName}.pickFiles`);
  return ts.transpileModule(callback.getText(page), {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText;
}

const draftCallback = callbackSource("DraftChat");
const chatCallback = callbackSource("ChatView");
const apiSource = readFileSync(new URL("./api.ts", import.meta.url), "utf8");
const apiTree = ts.createSourceFile("api.ts", apiSource, ts.ScriptTarget.Latest, true);
const errorClass = apiTree.statements.find((node) => ts.isClassDeclaration(node) && node.name?.text === "ApiError");
const errorModule = { exports: {} };
if (errorClass) {
  runInNewContext(
    ts.transpileModule(errorClass.getText(apiTree), {
      compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
    }).outputText,
    { module: errorModule, exports: errorModule.exports, Error },
  );
}
const { ApiError } = errorModule.exports;

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

// Model the default Chinese locale while retaining parameter interpolation.
const uiText = (key, params = {}) => key.replace(/\{(\w+)\}/g, (match, name) => Object.hasOwn(params, name) ? String(params[name]) : match);

function draftHarness(options = {}) {
  const creates = [];
  const uploads = [];
  const handoffs = [];
  const toasts = [];
  const uploadingStates = [];
  const create = deferred();
  const upload = deferred();
  const api = {
    createConversation(...args) {
      creates.push(args);
      return create.promise;
    },
    chatUpload(...args) {
      uploads.push(args);
      return upload.promise;
    },
  };
  const pickFiles = runInNewContext(`${draftCallback}\npickFiles`, {
    api,
    uiText,
    ApiError,
    agentKey: "auto",
    llmProfileId: 7,
    input: "  keep this unsent draft\n",
    uploading: false,
    sending: false,
    ...options,
    setUploading: (value) => uploadingStates.push(value),
    toast: { error: (message) => toasts.push(message) },
    onStarted: (conversation, pending) => handoffs.push({ conversation, pending }),
  });
  return { pickFiles, create, upload, creates, uploads, handoffs, toasts, uploadingStates };
}

const files = [new File(["one"], "one.txt"), new File(["two"], "two.txt")];
const conversation = { id: 42, agent_key: "auto" };

async function createForUpload(harness) {
  const done = harness.pickFiles(files);
  assert.deepEqual(harness.creates, [["auto", "", 7]]);
  assert.deepEqual(harness.uploadingStates, [true]);
  assert.equal(harness.uploads.length, 0);
  assert.equal(harness.handoffs.length, 0);
  harness.create.resolve(conversation);
  for (let turn = 0; turn < 10 && harness.uploads.length === 0; turn++) await Promise.resolve();
  assert.deepEqual(harness.uploads, [["session", "conv-42", files]]);
  assert.equal(harness.handoffs.length, 0);
  return { done };
}

test("failed conversation creation keeps the draft without uploading or handing off", async () => {
  const harness = draftHarness();
  const done = harness.pickFiles(files);
  harness.create.reject(new Error("creation unavailable"));
  await done;
  assert.equal(harness.creates.length, 1);
  assert.equal(harness.uploads.length, 0);
  assert.equal(harness.handoffs.length, 0);
  assert.deepEqual(harness.uploadingStates, [true, false]);
  assert.deepEqual(harness.toasts, ["上传失败：creation unavailable"]);
});

test("upload failure hands the existing conversation and original draft to ChatView for retry", async () => {
  const harness = draftHarness();
  const { done } = await createForUpload(harness);
  harness.upload.reject(new Error("upload unavailable"));
  await done;
  assert.equal(harness.creates.length, 1);
  assert.equal(harness.handoffs.length, 1);
  const [{ conversation: selected, pending }] = harness.handoffs;
  assert.equal(selected, conversation);
  assert.equal(pending.input, "  keep this unsent draft\n");
  assert.equal(pending.attachments.length, 0);
  assert.deepEqual(harness.uploadingStates, [true, false]);
  assert.deepEqual(harness.toasts, ["上传失败：upload unavailable"]);

  const retryUploads = [];
  const attachments = [{ name: "one.txt", path: "uploads/one.txt", size: 3 }];
  let queued = pending.attachments;
  const retry = runInNewContext(`${chatCallback}\npickFiles`, {
    uiText,
    conv: selected,
    api: {
      async chatUpload(...args) {
        retryUploads.push(args);
        return { attachments };
      },
    },
    setUploading: () => undefined,
    setAttachments: (update) => {
      queued = update(queued);
    },
    toast: { error: assert.fail },
  });
  await retry(files);
  assert.deepEqual(retryUploads, [["session", "conv-42", files]]);
  assert.deepEqual(Array.from(queued), attachments);
  assert.equal(harness.creates.length, 1);
});

test("an upload 401 leaves navigation to the API login redirect without conversation handoff", async () => {
  assert.equal(typeof ApiError, "function");
  const harness = draftHarness();
  const { done } = await createForUpload(harness);
  harness.upload.reject(new ApiError(401, "localized auth error"));
  await done;
  assert.equal(harness.handoffs.length, 0);
  assert.deepEqual(harness.uploadingStates, [true, false]);
});

test("a non-401 HTTP upload failure still hands off regardless of localized error text", async () => {
  const harness = draftHarness();
  const { done } = await createForUpload(harness);
  harness.upload.reject(new ApiError(503, "localized auth error"));
  await done;
  assert.equal(harness.handoffs.length, 1);
  assert.equal(harness.handoffs[0].conversation, conversation);
  assert.equal(harness.handoffs[0].pending.input, "  keep this unsent draft\n");
  assert.equal(harness.handoffs[0].pending.attachments.length, 0);
});

test("successful upload hands off once with the original draft and returned attachments", async () => {
  const harness = draftHarness();
  const { done } = await createForUpload(harness);
  const attachments = [{ name: "one.txt", path: "uploads/one.txt", size: 3 }];
  harness.upload.resolve({ attachments });
  await done;
  assert.equal(harness.creates.length, 1);
  assert.equal(harness.handoffs.length, 1);
  assert.equal(harness.handoffs[0].conversation, conversation);
  assert.equal(harness.handoffs[0].pending.input, "  keep this unsent draft\n");
  assert.equal(harness.handoffs[0].pending.attachments, attachments);
  assert.equal(harness.toasts.length, 0);
});

for (const [name, options, picked] of [
  ["empty file selection", {}, []],
  ["missing agent", { agentKey: "" }, files],
  ["upload in progress", { uploading: true }, files],
  ["send in progress", { sending: true }, files],
]) {
  test(`${name} has no creation, upload, state or handoff effects`, async () => {
    const harness = draftHarness(options);
    await harness.pickFiles(picked);
    assert.equal(harness.creates.length, 0);
    assert.equal(harness.uploads.length, 0);
    assert.equal(harness.handoffs.length, 0);
    assert.equal(harness.toasts.length, 0);
    assert.equal(harness.uploadingStates.length, 0);
  });
}
