'use strict';

const { Agent, ProxyAgent, Socks5ProxyAgent, buildConnector, fetch } = require('undici');

function configError(message) {
  const error = new Error(message);
  error.code = 'HARVEST_PROXY_CONFIG';
  return error;
}

function parseProxy(raw) {
  if (!raw) return null;
  try {
    const url = new URL(raw);
    if (!['http:', 'https:', 'socks5:'].includes(url.protocol) || !url.hostname ||
        (url.pathname && url.pathname !== '/') || url.search || url.hash) throw new Error();
    // Validate encoding here, so dependency errors cannot expose raw userinfo.
    decodeURIComponent(url.username);
    decodeURIComponent(url.password);
    return url;
  } catch {
    throw configError('Invalid proxy: use an HTTP, HTTPS, or SOCKS5 URL with a host and optional port.');
  }
}

function browserProxyArgs(raw) {
  const proxy = parseProxy(raw);
  if (!proxy) return [];
  // Chromium does not accept userinfo in --proxy-server. page.authenticate()
  // would also answer target auth challenges with these proxy credentials.
  if (proxy.username || proxy.password) {
    throw configError('Authenticated proxies are unsupported by the harvester browser; use a local unauthenticated proxy.');
  }
  return [`--proxy-server=${proxy.protocol}//${proxy.host}`, '--proxy-bypass-list=<-loopback>'];
}

function createForwarder({ proxy: raw = '', insecureTLS = false } = {}) {
  const proxy = parseProxy(raw);
  const tls = { rejectUnauthorized: insecureTLS !== true };
  const pendingSockets = new Set();
  let closed = false;
  const connect = buildConnector({});
  // SOCKS5 negotiation sockets are not yet owned by the dependency's pools.
  // Track the standard connector's sockets so teardown also aborts a greeting
  // that never completes (dispatcher.destroy alone cannot reach those sockets).
  const socksConnect = (options, callback) => {
    if (closed) { callback(new Error('Harvester transport closed.')); return; }
    const socket = connect(options, callback);
    pendingSockets.add(socket);
    socket.once('close', () => pendingSockets.delete(socket));
    return socket;
  };
  let dispatcher;
  try {
    dispatcher = !proxy ? new Agent({ connect: tls }) :
      proxy.protocol === 'socks5:' ? new Socks5ProxyAgent(proxy, { requestTls: tls, connect: socksConnect }) :
        new ProxyAgent({ uri: proxy, proxyTunnel: false, requestTls: tls, proxyTls: tls });
  } catch {
    throw configError('Could not initialize the harvester transport; check proxy configuration.');
  }
  return {
    async fetch(url, options) {
      try {
        return await fetch(url, { ...options, dispatcher });
      } catch {
        // Do not retain dependency errors/causes: they can contain proxy URLs.
        throw new Error('Harvester forwarding failed; check the proxy, target, and TLS configuration.');
      }
    },
    // Destroy also terminates unfinished intercepted requests during teardown.
    close: () => {
      closed = true;
      for (const socket of pendingSockets) socket.destroy(new Error('Harvester transport closed.'));
      return dispatcher.destroy();
    }
  };
}

module.exports = { browserProxyArgs, createForwarder };
