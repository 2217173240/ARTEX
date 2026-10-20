package selfupdate

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNewClientRejectsMalformedProxyBeforeNetwork(t *testing.T) {
	client, err := NewClient("http://fixture-user:fixture-secret@127.0.0.1:%zz")
	if err == nil || client != nil {
		t.Fatalf("invalid configured proxy must return an error and no usable client, got client=%v err=%v", client, err)
	}
	if strings.Contains(err.Error(), "fixture-user") || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("proxy validation error exposed credentials")
	}
}

func TestNewClientProxyValidation(t *testing.T) {
	for _, proxy := range []string{
		"127.0.0.1:8080", "//proxy.test:8080", "http:proxy.test", "ftp://proxy.test:8080",
		"http://", "http://:8080", "http:///proxy.test", "http://proxy.test:",
		"http://proxy.test:0", "http://proxy.test:65536", "http://proxy.test:no-port",
		"http://[invalid]:80", "http://::1", "http://2001:db8::1:80", "http://[::1", "http://proxy]", "http://proxy.test/path",
		"http://proxy.test?", "http://proxy.test?query=1", "http://proxy.test#", "http://proxy.test#fragment",
		"http://fixture-user:fixture-secret@proxy.test:%zz", "ftp://fixture-user:fixture-secret@proxy.test",
	} {
		t.Run(proxy, func(t *testing.T) {
			client, err := NewClient(proxy)
			if err == nil || client != nil {
				t.Fatalf("accepted invalid proxy %q", proxy)
			}
			if strings.Contains(err.Error(), "fixture-user") || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatal("proxy validation error exposed credentials")
			}
		})
	}
}

func TestNewClientPreservesSupportedProxyURLs(t *testing.T) {
	for _, proxy := range []string{
		"http://proxy.test", "https://proxy.test", "socks5://proxy.test", "socks5h://proxy.test",
		"http://127.0.0.1:8080/", "https://[::1]:443", "socks5://[fe80::1%25en0]:1080",
		"socks5h://fixture-user:fixture%3Asecret@proxy.test:1080",
	} {
		t.Run(proxy, func(t *testing.T) {
			client, err := NewClient(" \t" + proxy + "\n")
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			tr := client.Transport.(*http.Transport)
			if tr.Proxy == nil {
				t.Fatal("configured proxy became direct transport")
			}
			req, _ := http.NewRequest(http.MethodGet, latestURL, nil)
			got, err := tr.Proxy(req)
			if err != nil || got == nil || got.String() != proxy {
				t.Fatalf("proxy URL changed: %v %v", got, err)
			}
			if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
				t.Fatal("proxy disabled certificate verification")
			}
		})
	}
}

func TestNewClientEmptyProxyStaysDirect(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://unused.invalid:8080")
	t.Setenv("HTTPS_PROXY", "http://unused.invalid:8080")
	for _, proxy := range []string{"", " \t\n"} {
		client, err := NewClient(proxy)
		if err != nil {
			t.Fatal(err)
		}
		defer client.CloseIdleConnections()
		if client.Transport.(*http.Transport).Proxy != nil {
			t.Fatal("empty configured proxy must stay direct regardless of environment")
		}
	}
}

func TestNewClientFetchLatestAndAssetsThroughProxy(t *testing.T) {
	var originRequests, proxyRequests atomic.Int32
	payload := "local synthetic release payload"
	wantSum := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originRequests.Add(1)
		switch {
		case r.Host == "api.github.com":
			_, _ = io.WriteString(w, `{"tag_name":"v99.0.0"}`)
		case r.Host == "github.com" && strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
			_, _ = io.WriteString(w, wantSum+"  fixture.zip\n")
		case r.Host == "github.com" && strings.HasSuffix(r.URL.Path, "/fixture.zip"):
			_, _ = io.WriteString(w, payload)
		default:
			t.Errorf("unexpected origin host %q", r.Host)
		}
	}))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests.Add(1)
		if r.Method != http.MethodConnect || (r.Host != "api.github.com:443" && r.Host != "github.com:443") {
			t.Errorf("unexpected proxy request %s %s", r.Method, r.Host)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		upstream, err := net.Dial("tcp", origin.Listener.Addr().String())
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.Close()
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
		go func() { _, _ = io.Copy(upstream, rw) }()
		_, _ = io.Copy(conn, upstream)
	}))
	defer proxy.Close()
	client, err := NewClient(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	tr := client.Transport.(*http.Transport)
	tr.TLSClientConfig = origin.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tr.TLSClientConfig.ServerName = origin.Certificate().DNSNames[0]
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != proxy.Listener.Addr().String() {
			return nil, errors.New("attempted direct connection instead of configured proxy")
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	rel, err := FetchLatest(t.Context(), client)
	if err != nil || rel.TagName != "v99.0.0" {
		t.Fatalf("proxied release fetch: rel=%v err=%v", rel, err)
	}
	asset := Asset{Name: "fixture.zip", URL: "https://github.com/fixture/fixture.zip", Size: int64(len(payload))}
	rel.Assets = []Asset{asset, {Name: sumsAsset, URL: "https://github.com/fixture/SHA256SUMS"}}
	sums, err := fetchSums(t.Context(), client, rel)
	if err != nil || sums[asset.Name] != wantSum {
		t.Fatalf("proxied checksum fetch: sums=%v err=%v", sums, err)
	}
	downloadFile, err := os.Create(filepath.Join(t.TempDir(), "download.part"))
	if err != nil {
		t.Fatal(err)
	}
	defer downloadFile.Close()
	gotSum, err := download(t.Context(), client, asset, downloadFile, maxZIPSize, func(Phase, int, string) {})
	if err != nil || gotSum != sums[asset.Name] {
		t.Fatalf("proxied asset download: checksum=%v err=%v", gotSum, err)
	}
	if proxyRequests.Load() != 2 || originRequests.Load() != 3 {
		t.Fatalf("requests: proxy=%d origin=%d", proxyRequests.Load(), originRequests.Load())
	}
}

func TestNewClientRetainsRedirectPolicy(t *testing.T) {
	client, err := NewClient("http://proxy.test:8080")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	for _, raw := range []string{"http://github.com/fixture", "https://evil.test/fixture"} {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		if err := client.CheckRedirect(req, nil); err == nil {
			t.Fatalf("allowed unsafe redirect %s", raw)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://release-assets.githubusercontent.com/fixture", nil)
	if err := client.CheckRedirect(req, nil); err != nil {
		t.Fatalf("rejected allowed GitHub redirect: %v", err)
	}
	if err := client.CheckRedirect(req, make([]*http.Request, 10)); err == nil {
		t.Fatal("allowed excessive redirects")
	}
}

func TestNewClientUnavailableProxyDoesNotFallBack(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	calls := 0
	client.Transport.(*http.Transport).DialContext = func(_ context.Context, _, addr string) (net.Conn, error) {
		calls++
		if addr != "127.0.0.1:8080" {
			t.Errorf("unexpected direct dial %q", addr)
		}
		return nil, errors.New("local fixture proxy unavailable")
	}
	if _, err := FetchLatest(t.Context(), client); err == nil {
		t.Fatal("unavailable proxy unexpectedly succeeded")
	}
	if calls != 1 {
		t.Fatalf("expected one proxy dial and no fallback, got %d dials", calls)
	}
}
