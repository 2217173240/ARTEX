import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { runInNewContext } from "node:vm";

const libDir = path.dirname(fileURLToPath(import.meta.url));

const apiSource = readFileSync(new URL("./api.ts", import.meta.url), "utf8");
const apiTree = ts.createSourceFile("api.ts", apiSource, ts.ScriptTarget.Latest, true);
const errorClass = apiTree.statements.find((node) => ts.isClassDeclaration(node) && node.name?.text === "ApiError");
assert.ok(errorClass, "missing ApiError class in api.ts");

const errorModule = { exports: {} };
runInNewContext(
  ts.transpileModule(errorClass.getText(apiTree), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
  }).outputText,
  { module: errorModule, exports: errorModule.exports, Error },
);
const { ApiError } = errorModule.exports;

const helperModule = { exports: {} };
runInNewContext(
  ts.transpileModule(readFileSync(new URL("./archive-restore.ts", import.meta.url), "utf8"), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
  }).outputText,
  {
    module: helperModule,
    exports: helperModule.exports,
    Error,
    require(name) {
      assert.equal(name, "@/lib/api");
      return { ApiError };
    },
  },
);
const { isArchiveRestoreMissingError } = helperModule.exports;

function loadMockHandle() {
  const cache = new Map();
  function loadTS(filename) {
    if (cache.has(filename)) return cache.get(filename).exports;
    const module = { exports: {} };
    cache.set(filename, module);
    const output = ts.transpileModule(readFileSync(filename, "utf8"), {
      compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
    }).outputText;
    runInNewContext(output, {
      module,
      exports: module.exports,
      Error,
      URLSearchParams,
      structuredClone,
      setTimeout: (callback) => {
        callback();
        return 1;
      },
      require: (name) => loadTS(path.resolve(path.dirname(filename), `${name}.ts`)),
    });
    return module.exports;
  }
  return loadTS(path.join(libDir, "mock/handler.ts")).mockHandle;
}

test("isArchiveRestoreMissingError detects HTTP 404 regardless of error message locale", () => {
  assert.equal(isArchiveRestoreMissingError(new ApiError(404, "归档不存在")), true);
  assert.equal(isArchiveRestoreMissingError(new ApiError(404, "archive not found")), true);
  assert.equal(isArchiveRestoreMissingError(new ApiError(500, "internal error")), false);
  assert.equal(isArchiveRestoreMissingError(new Error("network failed")), false);
  assert.equal(isArchiveRestoreMissingError(null), false);
  assert.equal(isArchiveRestoreMissingError({ status: 404 }), false);
});

test("mock GET /task-archives/:id 404 is recognized by isArchiveRestoreMissingError", async () => {
  const mockHandle = loadMockHandle();
  await assert.rejects(
    mockHandle("GET", "/task-archives/999999"),
    (error) => isArchiveRestoreMissingError(error) === true && error.status === 404,
  );
});
