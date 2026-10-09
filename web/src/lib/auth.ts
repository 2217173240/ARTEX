export interface CurrentUser {
  id: string;
  name: string;
  username: string;
  email: string;
  avatar: string;
  role: string;
}
const demo: CurrentUser = { id: "1", name: "ARTEX", username: "ARTEX", email: "", avatar: "", role: "operator" };
let user: CurrentUser | null = null;
let generation = 0;
const SESSION_EPOCH_KEY = "artex_session_epoch";
function sessionGeneration(): string {
  const epoch = typeof window === "undefined" ? "" : (localStorage.getItem(SESSION_EPOCH_KEY) ?? "");
  return `${generation}:${epoch}`;
}
function advanceSession() {
  generation++;
  if (typeof window !== "undefined") {
    // This is a public change marker, including for HTTP LAN development.
    const epoch = globalThis.crypto?.getRandomValues
      ? Array.from(globalThis.crypto.getRandomValues(new Uint32Array(4))).join("-")
      : `${Date.now()}-${Math.random()}`;
    localStorage.setItem(SESSION_EPOCH_KEY, epoch);
  }
}
function removeLegacyToken() {
  if (typeof window === "undefined") return;
  localStorage.removeItem("artex_token");
  // Only the pre-upgrade readable cookie is visible here; HttpOnly sessions stay intact.
  if (document.cookie.split(";").some((cookie) => cookie.trim().startsWith("artex_token="))) {
    document.cookie = "artex_token=; path=/; max-age=0; SameSite=Lax";
  }
}
export const auth = {
  generation: sessionGeneration,
  getCurrentUser: () => user,
  async loadSession(): Promise<CurrentUser | null> {
    removeLegacyToken();
    if (process.env.NEXT_PUBLIC_MOCK === "1") {
      user = demo;
      return user;
    }
    const started = sessionGeneration();
    const response = await fetch("/api/auth/session", { credentials: "include", cache: "no-store" });
    if (response.status !== 401 && !response.ok) throw new Error("无法检查登录状态");
    const result = response.ok ? ((await response.json()) as { user: CurrentUser }).user : null;
    if (started !== sessionGeneration()) return this.loadSession();
    user = result;
    return user;
  },
  async signedIn(): Promise<void> {
    advanceSession();
    await this.loadSession();
  },
  async logout(): Promise<void> {
    if (process.env.NEXT_PUBLIC_MOCK !== "1") {
      const response = await fetch("/api/auth/logout", { method: "POST", credentials: "include" });
      if (!response.ok) throw new Error("退出登录失败");
    }
    advanceSession();
    user = null;
    removeLegacyToken();
  },
  async handleUnauthorized(started: string): Promise<void> {
    if (started !== sessionGeneration()) return;
    const current = await this.loadSession();
    if (!current && started === sessionGeneration() && typeof window !== "undefined") window.location.href = "/login";
  },
};
