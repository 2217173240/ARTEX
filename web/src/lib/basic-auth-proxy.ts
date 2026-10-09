// Credentials may go only to this app's origin or a explicitly configured local
// Go process. Redirects are never followed by the caller.
export function basicAuthCheckURL(requestURL: string, backend: string): URL | null {
  try {
    const source = new URL(requestURL);
    const target = new URL(backend);
    const loopback = ["localhost", "127.0.0.1", "[::1]"].includes(target.hostname);
    if (target.username || target.password || target.search || target.hash ||
        !["http:", "https:"].includes(target.protocol) ||
        (target.origin !== source.origin && !(loopback && target.protocol === "http:"))) return null;
    return new URL("/api/basic-auth/check", target);
  } catch { return null; }
}
