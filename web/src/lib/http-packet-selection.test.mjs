import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import ts from "typescript";

const compiled = ts.transpileModule(
  readFileSync(new URL("./http-packet-selection.ts", import.meta.url), "utf8"),
  { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } },
).outputText;
const exports = {};
runInNewContext(compiled, { exports });
const { packetLines, selectedPacketText } = exports;
// VM-created objects have a separate prototype; compare their serializable values.
const lines = (value) => JSON.parse(JSON.stringify(packetLines(value)));

test("packet offsets preserve CRLF, LF, lone CR, empty rows and trailing endings", () => {
  assert.deepEqual(lines("a\r\n\nb\rc\n"), [
    { text: "a", start: 0 }, { text: "", start: 3 },
    { text: "b", start: 4 }, { text: "c", start: 6 }, { text: "", start: 8 },
  ]);
  assert.deepEqual(lines(""), [{ text: "", start: 0 }]);
});

test("missing or collapsed selection leaves native copy alone", () => {
  assert.equal(selectedPacketText(null, "abc", null), null);
  assert.equal(selectedPacketText(null, "abc", { isCollapsed: true }), null);
});
