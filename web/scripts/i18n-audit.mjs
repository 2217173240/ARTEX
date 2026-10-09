import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";
const require = createRequire(import.meta.url);
const ts = require("typescript");
const root = path.resolve(path.dirname(new URL(import.meta.url).pathname), "../src");
const dictionary = JSON.parse(fs.readFileSync(path.join(root, "lib/i18n/en.json"), "utf8"));
const walk = (dir) => fs.readdirSync(dir, { withFileTypes: true }).flatMap((entry) => entry.isDirectory() ? walk(path.join(dir, entry.name)) : [path.join(dir, entry.name)]);
const missing = [];
const untranslatedMarkup = [];
let calls = 0;
for (const filename of walk(root).filter((file) => /\.tsx?$/.test(file) && !file.includes("/mock/"))) {
  const source = fs.readFileSync(filename, "utf8");
  const tree = ts.createSourceFile(filename, source, ts.ScriptTarget.Latest, true, filename.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
  const location = (node) => `${path.relative(root, filename)}:${tree.getLineAndCharacterOfPosition(node.getStart(tree)).line + 1}`;
  function visit(node) {
    if (ts.isCallExpression(node) && ["t", "uiText"].includes(node.expression.getText(tree)) && ts.isStringLiteral(node.arguments[0])) {
      calls++;
      const key = node.arguments[0].text;
      if (/[\u4e00-\u9fff]/.test(key) && !Object.hasOwn(dictionary, key)) missing.push({ location: location(node), key });
    }
    if (ts.isJsxText(node) && /[\u4e00-\u9fff]/.test(node.text)) untranslatedMarkup.push({ location: location(node), text: node.text.trim() });
    ts.forEachChild(node, visit);
  }
  visit(tree);
}
console.log(JSON.stringify({ staticTranslationCalls: calls, missingDictionaryKeys: missing, untranslatedMarkup }, null, 2));
if (missing.length) process.exitCode = 1;
