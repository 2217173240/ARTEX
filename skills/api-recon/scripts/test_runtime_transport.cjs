'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const https = require('node:https');
const net = require('node:net');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn, execFileSync } = require('node:child_process');
const { browserProxyArgs, createForwarder } = require('./runtime_transport');

// Generate a disposable synthetic identity: no stored private keys or user config.
let tls;
test.before(() => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'artex-harvest-tls-'));
  try {
    const key = path.join(dir, 'key.pem'), cert = path.join(dir, 'cert.pem');
    execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes',
      '-keyout', key, '-out', cert, '-days', '1', '-subj', '/CN=localhost',
      '-addext', 'subjectAltName=DNS:localhost,IP:127.0.0.1'], { stdio: 'ignore' });
    tls = { key: fs.readFileSync(key), cert: fs.readFileSync(cert) };
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

async function listen(t, server) {
  const sockets = new Set();
  server.on('connection', socket => {
    sockets.add(socket);
    socket.on('close', () => sockets.delete(socket));
    socket.on('error', () => {});
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(async () => {
    for (const socket of sockets) socket.destroy();
    await new Promise(resolve => server.close(resolve));
  });
  return server.address().port;
}

async function target(t, secure = false) {
  const requests = [];
  const handler = async (req, res) => {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const entry = { method: req.method, url: req.url, headers: req.headers, body: Buffer.concat(chunks).toString() };
    requests.push(entry);
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify(entry));
  };
  const server = secure ? https.createServer(tls, handler) : http.createServer(handler);
  const port = await listen(t, server);
  return { port, url: `${secure ? 'https' : 'http'}://127.0.0.1:${port}`, requests };
}

async function proxy(t, allowedPorts, secure = false) {
  const requests = [], tunnels = [], auth = [];
  const server = secure ? https.createServer(tls) : http.createServer();
  const allowed = url => ['localhost', '127.0.0.1'].includes(url.hostname) && allowedPorts.includes(Number(url.port));
  server.on('request', (req, res) => {
    let url;
    try { url = new URL(req.url); } catch { res.writeHead(400).end(); return; }
    // Chromium background traffic can never escape this synthetic fixture.
    if (!allowed(url)) { res.writeHead(403).end(); return; }
    requests.push(req.url);
    auth.push(req.headers['proxy-authorization']);
    const headers = { ...req.headers, 'x-fixture-proxy': 'yes' };
    delete headers['proxy-authorization'];
    const upstream = http.request(url, { method: req.method, headers }, r => {
      res.writeHead(r.statusCode, r.headers);
      r.pipe(res);
    });
    upstream.on('error', () => res.destroy());
    req.pipe(upstream);
  });
  server.on('connect', (req, client, head) => {
    let url;
    try { url = new URL(`http://${req.url}`); } catch { client.destroy(); return; }
    if (!allowed(url)) { client.end('HTTP/1.1 403 Forbidden\r\n\r\n'); return; }
    tunnels.push(req.url);
    auth.push(req.headers['proxy-authorization']);
    const upstream = net.connect(Number(url.port), '127.0.0.1', () => {
      client.write('HTTP/1.1 200 Connection Established\r\n\r\n');
      if (head.length) upstream.write(head);
      client.pipe(upstream).pipe(client);
    });
    upstream.on('error', () => client.destroy());
    client.on('close', () => upstream.destroy());
    upstream.on('close', () => client.destroy());
  });
  const port = await listen(t, server);
  return { url: `${secure ? 'https' : 'http'}://127.0.0.1:${port}`, requests, tunnels, auth };
}

function forwarder(t, options) {
  const transport = createForwarder(options);
  t.after(() => transport.close());
  return transport;
}

test('HTTP proxy carries method, headers, body and response; proxy auth stays at proxy', async t => {
  const upstream = await target(t);
  const relay = await proxy(t, [upstream.port]);
  const transport = forwarder(t, { proxy: relay.url.replace('://', '://fixture:secret@') });
  const response = await transport.fetch(`${upstream.url}/api/fixture?x=1`, {
    method: 'POST', headers: { 'content-type': 'application/json', 'x-fixture': 'request' }, body: '{"hello":"world"}'
  });
  assert.equal(response.status, 200);
  assert.equal(response.headers.get('content-type'), 'application/json');
  const received = await response.json();
  assert.equal(received.method, 'POST');
  assert.equal(received.body, '{"hello":"world"}');
  assert.equal(received.headers['x-fixture'], 'request');
  assert.equal(received.headers['x-fixture-proxy'], 'yes');
  assert.equal(received.headers['proxy-authorization'], undefined);
  assert.equal(relay.requests.length, 1);
  assert.equal(relay.auth[0], `Basic ${Buffer.from('fixture:secret').toString('base64')}`);
});

test('HTTPS target uses CONNECT; default rejects its certificate; opt-in succeeds', async t => {
  const upstream = await target(t, true);
  const relay = await proxy(t, [upstream.port]);
  const strict = forwarder(t, { proxy: relay.url });
  await assert.rejects(strict.fetch(`${upstream.url}/api/tls`), /forwarding failed/);
  assert.equal(upstream.requests.length, 0);
  const permissive = forwarder(t, { proxy: relay.url, insecureTLS: true });
  const response = await permissive.fetch(`${upstream.url}/api/tls`);
  assert.equal((await response.json()).url, '/api/tls');
  assert.equal(relay.tunnels.length, 2);
});

test('HTTPS proxy certificate validates by default and opt-in applies to the proxy TLS hop', async t => {
  const upstream = await target(t);
  const relay = await proxy(t, [upstream.port], true);
  const strict = forwarder(t, { proxy: relay.url });
  await assert.rejects(strict.fetch(`${upstream.url}/api/proxy-tls`), /forwarding failed/);
  assert.equal(upstream.requests.length, 0);
  assert.equal(relay.requests.length + relay.tunnels.length, 0);
  const permissive = forwarder(t, { proxy: relay.url, insecureTLS: true });
  const response = await permissive.fetch(`${upstream.url}/api/proxy-tls`);
  assert.equal((await response.json()).url, '/api/proxy-tls');
  assert.equal(relay.requests.length + relay.tunnels.length, 1);
});

test('direct TLS is strict even with NODE_TLS_REJECT_UNAUTHORIZED=0; opt-in is local', async t => {
  const upstream = await target(t, true);
  const saved = process.env.NODE_TLS_REJECT_UNAUTHORIZED;
  process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0';
  t.after(() => {
    if (saved === undefined) delete process.env.NODE_TLS_REJECT_UNAUTHORIZED;
    else process.env.NODE_TLS_REJECT_UNAUTHORIZED = saved;
  });
  const strict = forwarder(t);
  await assert.rejects(strict.fetch(`${upstream.url}/api/direct-tls`), /forwarding failed/);
  assert.equal(upstream.requests.length, 0);
  const permissive = forwarder(t, { insecureTLS: true });
  assert.equal((await (await permissive.fetch(upstream.url)).json()).url, '/');
  assert.equal(process.env.NODE_TLS_REJECT_UNAUTHORIZED, '0');
});

test('failed proxy never falls back to a reachable direct target and errors omit credentials', async t => {
  const upstream = await target(t);
  const failed = http.createServer((req, res) => res.writeHead(502).end());
  failed.on('connect', (req, socket) => socket.end('HTTP/1.1 502 Bad Gateway\r\n\r\n'));
  const port = await listen(t, failed);
  const transport = forwarder(t, { proxy: `http://private-user:private-password@127.0.0.1:${port}` });
  const response = await transport.fetch(upstream.url);
  assert.equal(response.status, 502);
  await response.text();
  assert.equal(upstream.requests.length, 0);
  await transport.close();
  await assert.rejects(transport.fetch(upstream.url), error => {
    assert.equal(error.cause, undefined);
    assert.doesNotMatch(String(error), /private-user|private-password/);
    return true;
  });
});

test('connection refusal does not fall back to direct and strips dependency error details', async t => {
  const upstream = await target(t);
  const closed = net.createServer();
  await new Promise(resolve => closed.listen(0, '127.0.0.1', resolve));
  const port = closed.address().port;
  await new Promise(resolve => closed.close(resolve));
  const transport = forwarder(t, { proxy: `http://private-user:private-password@127.0.0.1:${port}` });
  await assert.rejects(transport.fetch(upstream.url), error => {
    assert.match(error.message, /forwarding failed/);
    assert.doesNotMatch(String(error), /private-user|private-password/);
    assert.equal(error.cause, undefined);
    return true;
  });
  assert.equal(upstream.requests.length, 0);
});

test('SOCKS5 carries HTTP and HTTPS through the configured proxy with target TLS validation', async t => {
  const plain = await target(t), secure = await target(t, true);
  const tunnels = [];
  const server = net.createServer(socket => {
    let buffer = Buffer.alloc(0), greeted = false;
    const onData = data => {
      buffer = Buffer.concat([buffer, data]);
      if (!greeted) {
        if (buffer.length < 2 || buffer.length < 2 + buffer[1]) return;
        buffer = buffer.subarray(2 + buffer[1]);
        greeted = true;
        socket.write(Buffer.from([5, 0]));
      }
      if (buffer.length < 5) return;
      let host, end;
      if (buffer[3] === 1) { end = 8; host = Array.from(buffer.subarray(4, 8)).join('.'); }
      else if (buffer[3] === 3) { end = 5 + buffer[4]; host = buffer.subarray(5, end).toString(); }
      else { socket.destroy(); return; }
      if (buffer.length < end + 2) return;
      const port = buffer.readUInt16BE(end);
      if (!['127.0.0.1', 'localhost'].includes(host) || ![plain.port, secure.port].includes(port)) { socket.destroy(); return; }
      tunnels.push({ host, port });
      socket.off('data', onData);
      const pending = buffer.subarray(end + 2);
      const upstream = net.connect(port, '127.0.0.1', () => {
        socket.write(Buffer.from([5, 0, 0, 1, 127, 0, 0, 1, 0, 0]));
        if (pending.length) upstream.write(pending);
        socket.pipe(upstream).pipe(socket);
      });
      upstream.on('error', () => socket.destroy());
      socket.on('close', () => upstream.destroy());
    };
    socket.on('data', onData);
  });
  const port = await listen(t, server);
  const strict = forwarder(t, { proxy: `socks5://127.0.0.1:${port}` });
  assert.equal((await (await strict.fetch(`${plain.url}/api/socks`)).json()).url, '/api/socks');
  await assert.rejects(strict.fetch(secure.url), /forwarding failed/);
  assert.equal(secure.requests.length, 0);
  const permissive = forwarder(t, { proxy: `socks5://127.0.0.1:${port}`, insecureTLS: true });
  assert.equal((await (await permissive.fetch(secure.url)).json()).url, '/');
  assert.equal(tunnels.length, 3);
});

test('invalid proxies and browser credentials fail before launch without echoing input', () => {
  for (const proxy of ['socks4://user:secret@localhost:10', 'http://user:secret@', 'http://user:secret@localhost/path']) {
    assert.throws(() => createForwarder({ proxy }), error => !/user|secret/.test(String(error)));
  }
  assert.throws(() => browserProxyArgs('http://user:secret@localhost:8080'), /Authenticated proxies are unsupported/);
  assert.deepEqual(browserProxyArgs('socks5://127.0.0.1:1080'), [
    '--proxy-server=socks5://127.0.0.1:1080', '--proxy-bypass-list=<-loopback>'
  ]);
});

test('close aborts an in-flight SOCKS5 greeting and closes its proxy socket', { timeout: 3000 }, async t => {
  let connected;
  const connection = new Promise(resolve => { connected = resolve; });
  let peerClosed;
  const peerClose = new Promise(resolve => { peerClosed = resolve; });
  const server = net.createServer(socket => {
    socket.once('data', connected); // Read but never answer the SOCKS greeting.
    socket.once('close', peerClosed);
  });
  const port = await listen(t, server);
  const transport = forwarder(t, { proxy: `socks5://127.0.0.1:${port}` });
  const request = assert.rejects(transport.fetch('http://127.0.0.1:12345/api/stalled'), /forwarding failed/);
  await connection;
  await transport.close();
  await Promise.all([request, peerClose]);
});

async function runHarvester(t, config) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'artex-harvest-test-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const file = path.join(dir, 'config.json');
  fs.writeFileSync(file, JSON.stringify(config));
  const child = spawn(process.execPath, [path.join(__dirname, 'runtime_harvest.js'), file], {
    env: { ...process.env, HTTP_PROXY: '', HTTPS_PROXY: '', ALL_PROXY: '' }
  });
  let output = '';
  child.stdout.on('data', data => { output += data; });
  child.stderr.on('data', data => { output += data; });
  const timer = setTimeout(() => child.kill('SIGKILL'), 25000);
  const code = await new Promise((resolve, reject) => { child.on('close', resolve); child.on('error', reject); });
  clearTimeout(timer);
  return { code, output, dir };
}

test('real entry rejects authenticated proxies early and safely', async t => {
  const result = await runHarvester(t, { baseUrl: 'http://127.0.0.1/', proxy: 'http://private-user:private-password@127.0.0.1:1' });
  assert.equal(result.code, 1);
  assert.doesNotMatch(result.output, /private-user|private-password/);
  assert.match(result.output, /Authenticated proxies are unsupported/);
});

test('real entry routes Chromium loopback and forwarded API via proxy, writes capture, then exits', {
  skip: !process.env.CHROMIUM && 'Set CHROMIUM to an existing system browser to run the integration fixture.'
}, async t => {
  const requests = [];
  const server = http.createServer(async (req, res) => {
    let body = '';
    for await (const chunk of req) body += chunk;
    requests.push({ url: req.url, viaProxy: req.headers['x-fixture-proxy'], body });
    if (req.url.startsWith('/api/')) {
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({ code: 401, data: { fixture: true, body } }));
    } else {
      res.setHeader('content-type', 'text/html');
      res.end('<html><script>fetch("/api/fixture", {method:"POST",headers:{"content-type":"application/json"},body:"fixture-body"})</script></html>');
    }
  });
  const port = await listen(t, server);
  const relay = await proxy(t, [port]);
  const result = await runHarvester(t, {
    baseUrl: `http://127.0.0.1:${port}/`, chromium: process.env.CHROMIUM,
    proxy: relay.url, waitMs: 300, perRouteMs: 300, routes: ['/'], recordWs: false
  });
  assert.equal(result.code, 0, result.output);
  const capture = JSON.parse(fs.readFileSync(path.join(result.dir, 'runtime_api.json')));
  assert.ok(capture.rec.some(entry => entry.resp && JSON.parse(entry.resp).data.body === 'fixture-body'));
  assert.ok(relay.requests.some(url => url.endsWith('/api/fixture')));
  assert.ok(requests.length >= 2);
  assert.ok(requests.every(req => req.viaProxy === 'yes'), JSON.stringify(requests));
});

test('real entry with a refused proxy never reaches the loopback target directly', {
  skip: !process.env.CHROMIUM && 'Set CHROMIUM to an existing system browser to run the integration fixture.'
}, async t => {
  const upstream = await target(t);
  const closed = net.createServer();
  await new Promise(resolve => closed.listen(0, '127.0.0.1', resolve));
  const port = closed.address().port;
  await new Promise(resolve => closed.close(resolve));
  const result = await runHarvester(t, {
    baseUrl: upstream.url, chromium: process.env.CHROMIUM,
    proxy: `http://127.0.0.1:${port}`, waitMs: 1, perRouteMs: 1, routes: ['/'], recordWs: false
  });
  assert.equal(result.code, 0, result.output);
  assert.equal(upstream.requests.length, 0);
  assert.match(result.output, /ERR_PROXY_CONNECTION_FAILED/);
});
