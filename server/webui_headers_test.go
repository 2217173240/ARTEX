package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestConsoleBrowserProtections verifies the webui wrapper sets frame/referrer
// protections on console responses, and that API responses don't carry them.
//
// 路由现实（与上游测试的差异）：裸 Server 即可拿到 Handler（注册不触碰状态，
// basicAuthGate 零值即关闭）；默认 no-embed 构建下 webuiHandler 是 404 stub，
// 所以对 `/` 与前端路由 `/settings/` 只断言三个防护头，不断言状态码——只要
// 请求走了 webui 分支，包装器先设的头就会随响应发出。`/api/health` 免 JWT
// （auth.go requireAuth）也免 Basic（basic_auth.go requireBasicAuth），handler
// 只读环境变量，裸 Server 下即真实 200，用它断言 API 响应不带这三个头。
func TestConsoleBrowserProtections(t *testing.T) {
	handler := (&Server{}).Handler()
	headers := map[string]string{
		"Content-Security-Policy": "frame-ancestors 'self'",
		"X-Frame-Options":         "SAMEORIGIN",
		"Referrer-Policy":         "same-origin",
	}

	for _, path := range []string{"/", "/settings/"} {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		sent := res.Result().Header
		for name, want := range headers {
			if got := sent.Get(name); got != want {
				t.Errorf("%s: %s = %q, want %q", path, name, got, want)
			}
		}
	}

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("health: status = %d, want %d", res.Code, http.StatusOK)
	}
	sent := res.Result().Header
	for name := range headers {
		if got := sent.Get(name); got != "" {
			t.Errorf("health: unexpected %s = %q", name, got)
		}
	}
}
