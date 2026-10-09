import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
const source = readFileSync(new URL('./basic-auth-proxy.ts', import.meta.url), 'utf8');
const { basicAuthCheckURL } = await import(`data:text/javascript;base64,${Buffer.from(ts.transpile(source, {module: ts.ModuleKind.ESNext})).toString('base64')}`);
test('credential boundary permits local Go and same origin, rejecting cross origin and credential-bearing targets', () => {
  assert.equal(basicAuthCheckURL('http://192.168.1.5:5173/login', 'http://localhost:8787').href, 'http://localhost:8787/api/basic-auth/check');
  assert.ok(basicAuthCheckURL('https://app.test/login', 'https://app.test'));
  for (const backend of ['https://evil.test', 'http://app.test:8787', 'http://user:pass@localhost:8787', 'file:///tmp/a', 'http://localhost:8787?x=y']) {
    assert.equal(basicAuthCheckURL('https://app.test/login', backend), null, backend);
  }
});
