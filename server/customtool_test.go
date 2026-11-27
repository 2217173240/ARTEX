package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRenderTemplateCommand(t *testing.T) {
	// command: string params shell-quoted; array → JSON (also quoted).
	got := renderTemplate("nmap -p {ports} {target}", map[string]any{
		"target": "10.0.0.1; rm -rf /", // injection attempt → must be single-quoted
		"ports":  []any{80.0, 443.0},
	}, shellQuote)
	if !strings.Contains(got, `'10.0.0.1; rm -rf /'`) {
		t.Fatalf("target not shell-quoted: %q", got)
	}
	if strings.Contains(got, "rm -rf /'") && !strings.Contains(got, `'10.0.0.1; rm -rf /'`) {
		t.Fatalf("possible injection leak: %q", got)
	}
	if strings.Contains(got, "{target}") || strings.Contains(got, "{ports}") {
		t.Fatalf("placeholders not replaced: %q", got)
	}
}

func TestRenderTemplateHTTP(t *testing.T) {
	// http: identity (no shell quoting) — raw substitution into url/body.
	// abc is a real param; braces inside the flag value must stay literal.
	got := renderTemplate("https://x/submit?flag={flag}", map[string]any{
		"flag": "CTF{abc}",
		"abc":  "nope",
	}, identity)
	if got != "https://x/submit?flag=CTF{abc}" {
		t.Fatalf("http render: %q", got)
	}
}

func TestRenderTemplateDoesNotRescanValues(t *testing.T) {
	got := renderTemplate("cmd {a} {missing}", map[string]any{
		"a": "{b}",
		"b": "; id",
	}, shellQuote)
	if got != "cmd '{b}' {missing}" {
		t.Fatalf("render = %q", got)
	}
}

func TestScalarStr(t *testing.T) {
	cases := []struct {
		v    any
		want string
		ok   bool
	}{
		{"hi", "hi", true},
		{true, "true", true},
		{float64(80), "80", true},   // integer-valued float → no decimal
		{float64(1.5), "1.5", true}, // real float
		{[]any{1, 2}, "", false},    // array → not scalar
		{map[string]any{}, "", false},
	}
	for _, c := range cases {
		got, ok := scalarStr(c.v)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("scalarStr(%v) = (%q,%v), want (%q,%v)", c.v, got, ok, c.want, c.ok)
		}
	}
}

func TestEnsureSchema(t *testing.T) {
	// empty → thin {args:string}
	m := ensureSchema(nil)
	props, _ := m["properties"].(map[string]any)
	if _, ok := props["args"]; !ok {
		t.Fatalf("empty schema should default to args: %v", m)
	}
	// non-empty → passthrough
	raw := json.RawMessage(`{"type":"object","properties":{"target":{"type":"string"}}}`)
	m2 := ensureSchema(raw)
	p2, _ := m2["properties"].(map[string]any)
	if _, ok := p2["target"]; !ok {
		t.Fatalf("non-empty schema should pass through: %v", m2)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("shellQuote(a'b) = %q", got)
	}
}

// TestExecPython runs a real Python script end-to-end: it must read params from
// stdin JSON and the mirrored env var, then print — verifying the whole script
// param-passing path. Skips if no python3.
func TestExecPython(t *testing.T) {
	interp, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	code := `import json,sys,os
a = json.load(sys.stdin)
print("stdin_target=" + a["target"])
print("env_target=" + os.environ.get("TOOL_TARGET",""))
print("port=" + str(a["port"]))
`
	out, err := execPython(context.Background(), interp, "test", code,
		map[string]any{"target": "example.com", "port": float64(8080)},
		t.TempDir(), nil, 10*time.Second)
	if err != nil {
		t.Fatalf("execPython error: %v", err)
	}
	for _, want := range []string{"stdin_target=example.com", "env_target=example.com", "port=8080"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

// TestRunHTTPTool functionally exercises the http executor against a local server:
// method/url/header/body templates are rendered from params, the request is sent,
// and the response status+body are returned. No recording proxy → s.m untouched.
func TestRunHTTPTool(t *testing.T) {
	var gotMethod, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	execRaw, _ := json.Marshal(map[string]any{
		"method":  "POST",
		"url":     srv.URL + "/submit?flag={flag}",
		"headers": map[string]string{"Authorization": "Bearer {token}"},
		"body":    `{"flag":"{flag}"}`,
	})
	res, err := (&Server{}).runHTTPTool(context.Background(), execRaw,
		map[string]any{"flag": "CTF{x}", "token": "sekret"}, nil)
	if err != nil {
		t.Fatalf("runHTTPTool: %v", err)
	}
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	var out struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	_ = json.Unmarshal([]byte(res.Content[0].Text), &out)

	if gotMethod != "POST" {
		t.Fatalf("method: %q", gotMethod)
	}
	if gotAuth != "Bearer sekret" {
		t.Fatalf("header template not rendered: %q", gotAuth)
	}
	if gotBody != `{"flag":"CTF{x}"}` {
		t.Fatalf("body template not rendered: %q", gotBody)
	}
	if out.Status != 201 || !strings.Contains(out.Body, `"ok":true`) {
		t.Fatalf("response not captured: %+v", out)
	}
}

func TestDetectPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	if p := detectPython(); p == "" {
		t.Fatal("detectPython returned empty despite python3 on PATH")
	}
}

// The http tool must truncate an oversized response through norma's Capture, the
// same valve every other tool uses — a session with no OutputDir falls back to a
// head+tail cut, so a body far past the default cap comes back shortened.
func TestHTTPToolTruncatesLargeBody(t *testing.T) {
	big := strings.Repeat("x", 40000) // past Capture's 30000 default
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()

	execRaw, _ := json.Marshal(map[string]any{"method": "GET", "url": srv.URL})
	res, err := (&Server{}).runHTTPTool(context.Background(), execRaw, map[string]any{}, nil)
	if err != nil {
		t.Fatalf("runHTTPTool: %v", err)
	}
	out := res.Flatten()
	if len(out) >= 40000 {
		t.Fatalf("oversized http body was not truncated: len=%d", len(out))
	}
}

func TestHttpProxyTransportNormalizesSchemeAndRoutesThroughRecordingProxy(t *testing.T) {
	t.Parallel()

	// 1. Scheme-prefixed ProxyAddr() must not produce "http://http://...".
	sWithScheme := &Server{m: &Manager{globalProxy: "http://127.0.0.1:18080"}}
	tr, err := sWithScheme.httpProxyTransport(httpExec{UseRecordingProxy: true})
	if err != nil {
		t.Fatalf("httpProxyTransport error: %v", err)
	}
	if tr == nil || tr.Proxy == nil {
		t.Fatal("expected non-nil proxy transport")
	}
	dummyReq, _ := http.NewRequest(http.MethodGet, "http://example.com/test", nil)
	pu, err := tr.Proxy(dummyReq)
	if err != nil {
		t.Fatalf("tr.Proxy error: %v", err)
	}
	if pu == nil || pu.String() != "http://127.0.0.1:18080" {
		t.Fatalf("proxy URL = %v, want http://127.0.0.1:18080", pu)
	}

	// 2. Bare host:port ProxyAddr() still gets "http://" prepended.
	sBare := &Server{m: &Manager{globalProxy: "127.0.0.1:18081"}}
	trBare, err := sBare.httpProxyTransport(httpExec{UseRecordingProxy: true})
	if err != nil {
		t.Fatalf("httpProxyTransport(bare) error: %v", err)
	}
	if trBare == nil || trBare.Proxy == nil {
		t.Fatal("expected non-nil proxy transport for bare addr")
	}
	puBare, err := trBare.Proxy(dummyReq)
	if err != nil || puBare == nil || puBare.String() != "http://127.0.0.1:18081" {
		t.Fatalf("bare proxy URL = %v (err=%v), want http://127.0.0.1:18081", puBare, err)
	}

	// 3. End-to-end: runHTTPTool with UseRecordingProxy=true and scheme-prefixed
	// ProxyAddr() routes the HTTP request through the local httptest proxy.
	targetHit := false
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("via-target"))
	}))
	defer targetSrv.Close()

	var proxiedURI string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxiedURI = r.RequestURI
		u, err := url.Parse(r.RequestURI)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fwdReq, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		resp, err := http.DefaultClient.Do(fwdReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxySrv.Close()

	execRaw, _ := json.Marshal(map[string]any{
		"method":              "GET",
		"url":                 targetSrv.URL + "/ping",
		"use_recording_proxy": true,
	})
	srv := &Server{m: &Manager{globalProxy: proxySrv.URL}}
	res, err := srv.runHTTPTool(context.Background(), execRaw, map[string]any{}, nil)
	if err != nil {
		t.Fatalf("runHTTPTool error: %v", err)
	}
	if res.IsError {
		t.Fatalf("runHTTPTool failed: %s", res.Flatten())
	}
	if !targetHit {
		t.Fatal("target server was not reached")
	}
	if proxiedURI != targetSrv.URL+"/ping" {
		t.Fatalf("proxy did not receive expected URI: got %q, want %q", proxiedURI, targetSrv.URL+"/ping")
	}
	if !strings.Contains(res.Flatten(), "via-target") {
		t.Fatalf("expected proxied response body, got: %s", res.Flatten())
	}
}

func TestHttpProxyTransportInvalidProxyFailsClosed(t *testing.T) {
	t.Parallel()

	var targetHits atomic.Int32
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	badProxies := []string{
		"ftp://alice:supersecret@127.0.0.1:21",
		"http://alice:supersecret@127.0.0.1:%zz",
		"http://alice:supersecret@",
		"://alice:supersecret@127.0.0.1:8080",
	}
	for _, proxy := range badProxies {
		t.Run(proxy, func(t *testing.T) {
			tr, err := (&Server{}).httpProxyTransport(httpExec{Proxy: proxy})
			if err == nil || tr != nil {
				t.Fatalf("httpProxyTransport(%q) = (%v, %v), want (nil, error)", proxy, tr, err)
			}
			for _, secret := range []string{"alice:supersecret", "alice", "supersecret"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("httpProxyTransport error %q leaked %q", err.Error(), secret)
				}
			}

			before := targetHits.Load()
			execRaw, _ := json.Marshal(map[string]any{
				"method": "GET",
				"url":    targetSrv.URL + "/probe",
				"proxy":  proxy,
			})
			res, rerr := (&Server{}).runHTTPTool(context.Background(), execRaw, map[string]any{}, nil)
			if rerr != nil {
				t.Fatalf("runHTTPTool returned Go error: %v", rerr)
			}
			if !res.IsError {
				t.Fatalf("runHTTPTool(%q) should fail closed, got: %s", proxy, res.Flatten())
			}
			if targetHits.Load() != before {
				t.Fatalf("runHTTPTool(%q) dialed target directly despite invalid proxy", proxy)
			}
			out := res.Flatten()
			for _, secret := range []string{"alice:supersecret", "alice", "supersecret"} {
				if strings.Contains(out, secret) {
					t.Errorf("runHTTPTool error %q leaked %q", out, secret)
				}
			}
		})
	}
}

func TestListModelsInvalidProxyFailsClosed(t *testing.T) {
	t.Parallel()

	var targetHits atomic.Int32
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer targetSrv.Close()

	badProxies := []string{
		"ftp://alice:supersecret@127.0.0.1:21",
		"http://alice:supersecret@127.0.0.1:%zz",
		"http://alice:supersecret@",
		"://alice:supersecret@127.0.0.1:8080",
	}
	for _, proxy := range badProxies {
		t.Run(proxy, func(t *testing.T) {
			before := targetHits.Load()
			body, _ := json.Marshal(map[string]string{
				"provider": "openai",
				"base_url": targetSrv.URL + "/v1",
				"api_key":  "test-key",
				"proxy":    proxy,
			})
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/llm/models", strings.NewReader(string(body)))
			(&Server{}).pgListModels(w, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("pgListModels(%q) status = %d, want 400; body=%s", proxy, w.Code, w.Body.String())
			}
			if targetHits.Load() != before {
				t.Fatalf("pgListModels(%q) dialed target directly despite invalid proxy", proxy)
			}
			respBody := w.Body.String()
			for _, secret := range []string{"alice:supersecret", "alice", "supersecret"} {
				if strings.Contains(respBody, secret) {
					t.Errorf("pgListModels response %q leaked %q", respBody, secret)
				}
			}
		})
	}
}
