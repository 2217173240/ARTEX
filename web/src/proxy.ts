import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { basicAuthCheckURL } from "@/lib/basic-auth-proxy";

const AUTH_PAGES = ["/login", "/setup"];

export async function proxy(request: NextRequest) {
  // Mock demo：无真实登录，放行所有页面（客户端 auth 守卫也会放行）。
  if (process.env.NEXT_PUBLIC_MOCK === "1") return NextResponse.next();

  const checkURL = basicAuthCheckURL(request.url, process.env.AUTOPENTEST_API ?? "http://localhost:8787");
  if (!checkURL) return new NextResponse("Invalid local API proxy configuration", { status: 503 });
  const headers = new Headers({ "X-Forwarded-Host": request.nextUrl.host,
    "X-Forwarded-Proto": request.nextUrl.protocol.slice(0, -1) });
  const gateCookie = request.cookies.get("artex_basic_auth");
  if (gateCookie) headers.set("Cookie", `artex_basic_auth=${gateCookie.value}`);
  const authorization = request.headers.get("authorization");
  if (authorization?.startsWith("Basic ")) headers.set("Authorization", authorization);
  let check: Response;
  try {
    check = await fetch(checkURL, { headers, cache: "no-store", redirect: "manual", signal: AbortSignal.timeout(10000) });
  } catch { return new NextResponse("API proxy unavailable", { status: 503 }); }
  if (check.status !== 204) {
    const response = new NextResponse(null, { status: check.status === 401 ? 401 : 503 });
    response.headers.set("Cache-Control", "no-store");
    if (check.status === 401) response.headers.set("WWW-Authenticate", 'Basic realm="ARTEX", charset="UTF-8"');
    return response;
  }
  const finish = (response: NextResponse) => {
    const cookie = check.headers.get("set-cookie");
    if (cookie?.startsWith("artex_basic_auth=")) response.headers.append("Set-Cookie", cookie);
    response.headers.set("Cache-Control", "no-store");
    return response;
  };
  const { pathname } = request.nextUrl;
  const token = request.cookies.get("artex_token")?.value;
  const isAuthPage = AUTH_PAGES.some((p) => pathname === p || pathname.startsWith(`${p}/`));

  // Assets share the gate, while the admin page redirect applies only to pages.
  if (pathname.startsWith("/_next/") || /\.(?:png|jpg|jpeg|gif|webp|svg|ico|woff2?|ttf|otf)$/.test(pathname)) return finish(NextResponse.next());

  // 未登录 → 跳转登录页
  if (!token && !isAuthPage) {
    return finish(NextResponse.redirect(new URL("/login", request.url)));
  }

  return finish(NextResponse.next());
}

export const config = {
  // API requests go through the Go gate; pages and static assets are checked here.
  matcher: ["/((?!api/).*)"],
};
