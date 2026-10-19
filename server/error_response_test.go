package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func assertErrorResponse(t *testing.T, w *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d", w.Code, status)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON error response: %v", err)
	}
	if len(body) != 1 || body["error"] != message {
		// Do not copy a potentially sensitive unexpected response into test logs.
		t.Fatal("error response did not match the expected single public message")
	}
}

func TestErrorResponseUnexpectedServerErrors(t *testing.T) {
	fixtures := []struct {
		name string
		text string
	}{
		{"path", "open /invented-private-workspace/reports: permission denied"},
		{"sql", "query SELECT confidential_column FROM invented_private_table failed"},
		{"dsn", "postgres://fixture:" + "invented-password@db.invalid/app?sslmode=disable"},
		{"token", "unrecognized-value=" + strings.Repeat("invented", 7)},
		{"upstream", "upstream body: arbitrary data that has no credential prefix"},
	}
	for _, status := range []int{500, 502, 503, 599} {
		for _, fixture := range fixtures {
			t.Run(strconv.Itoa(status)+"/"+fixture.name, func(t *testing.T) {
				w := httptest.NewRecorder()
				w.Header().Set("Retry-After", "5")
				writeErr(w, status, fixture.text)
				assertErrorResponse(t, w, status, "服务内部错误，请稍后重试")
				if w.Header().Get("Retry-After") != "5" {
					t.Fatal("existing response header was lost")
				}
			})
		}
	}
}

func TestErrorResponseEntireServerErrorRange(t *testing.T) {
	for status := 500; status <= 599; status++ {
		w := httptest.NewRecorder()
		writeErr(w, status, "unexpected diagnostic")
		assertErrorResponse(t, w, status, "服务内部错误，请稍后重试")
	}
}

func TestErrorResponseClientMessagesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		status  int
		message string
	}{
		{400, "缺少 SKILL.md"},
		{409, "已存在，请选择其他名称"},
	} {
		w := httptest.NewRecorder()
		writeErr(w, tc.status, tc.message)
		assertErrorResponse(t, w, tc.status, tc.message)
	}
}

func TestErrorResponseWorkspaceOpenFailure(t *testing.T) {
	// This invokes the real os.OpenRoot failure path without a DB or a user path.
	missing := filepath.Join(t.TempDir(), "missing-private-workspace")
	s := &Server{m: &Manager{dir: missing}}
	w := httptest.NewRecorder()
	s.wsList(w, httptest.NewRequest(http.MethodGet, "/api/workspace/list", nil))
	assertErrorResponse(t, w, http.StatusInternalServerError, "服务内部错误，请稍后重试")
	if strings.Contains(w.Body.String(), missing) {
		t.Fatal("workspace failure reflected the configured filesystem path")
	}
}

func TestErrorResponsePublicRecoveryMessages(t *testing.T) {
	// These are intentionally public recovery/partial-success contracts, even
	// though they use a 5xx status. They must survive the explicit public path.
	for _, tc := range []struct {
		name    string
		status  int
		message string
	}{
		{"chat-add-config", 503, "尚未配置 LLM：请到 系统 → LLM 配置 添加一个配置"},
		{"chat-activate-config", 503, "没有已激活的 LLM 配置：请到 系统 → LLM 配置 激活一个，或在本对话为该会话指定一个配置"},
		{"chat-readiness", 503, (&Server{m: &Manager{}}).chatUnavailableReason()},
		{"mcp-approval", 502, "工具发现失败，请检查授权和服务器配置"},
		{"worker-partial-save", 500, "Worker count was saved, but running task pools could not be synchronized. Retry the settings update."},
		{"goal-partial-create", 500, "目标写入后读取失败"},
		{"goal-partial-update", 500, "目标更新后读取失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writePublicErr(w, tc.status, tc.message)
			assertErrorResponse(t, w, tc.status, tc.message)
		})
	}
}

func TestErrorResponseAvailabilityHandlers(t *testing.T) {
	s := &Server{m: &Manager{tasks: map[string]*Task{"fixture": {ID: "fixture"}}}}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		message string
	}{
		{"auth-status", s.authStatus, errDataSourceUnavailable},
		{"management-db", func(w http.ResponseWriter, _ *http.Request) { s.pg(w) }, "管理后台数据源(PostgreSQL)未连接"},
		{"asset-store", s.taskCoverage, "asset store 未启用"},
		{"asset-db", s.listCompanies, "database unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.SetPathValue("id", "fixture")
			tc.handler(w, r)
			assertErrorResponse(t, w, http.StatusServiceUnavailable, tc.message)
		})
	}
}
