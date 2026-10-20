package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// observedTransport 保持真实 TLS 请求，仅记录响应体实际交付给下载器的字节数。
type observedTransport struct {
	base http.RoundTripper
	read atomic.Int64
}

func (o *observedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := o.base.RoundTrip(r)
	if err == nil && strings.HasSuffix(r.URL.Path, ".zip") {
		resp.Body = &observedBody{ReadCloser: resp.Body, read: &o.read}
	}
	return resp, err
}

type observedBody struct {
	io.ReadCloser
	read *atomic.Int64
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read.Add(int64(n))
	return n, err
}

func localTLSClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	c, err := NewClient("")
	if err != nil {
		t.Fatal(err)
	}
	tr := c.Transport.(*http.Transport)
	tr.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tr.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func TestDownloadTLSBoundedStream(t *testing.T) {
	const budget int64 = 16 << 10
	for _, tc := range []struct {
		name       string
		size, body int64
		wantRead   int64
		wantError  string
	}{
		{"known-exact", 1024, 1024, 1024, ""},
		{"known-short", 1024, 1023, 1023, "下载不完整"},
		{"known-over", 1024, budget * 8, 1025, "超过上限"},
		{"unknown-exact", 0, budget, budget, ""},
		{"unknown-over", 0, budget * 8, budget + 1, "超过上限"},
		{"negative-unknown-over", -1, budget * 8, budget + 1, "超过上限"},
		{"metadata-over", budget + 1, 1, 0, "声明大小"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := bytes.Repeat([]byte("x"), int(tc.body))
			var requests atomic.Int32
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = w.Write(payload)
			}))
			defer s.Close()
			c := localTLSClient(t, s)
			observer := &observedTransport{base: c.Transport}
			c.Transport = observer
			f, err := os.CreateTemp(t.TempDir(), "download-*")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			got, err := download(t.Context(), c, Asset{Name: "payload.zip", URL: "https://github.com/payload.zip", Size: tc.size}, f, budget, func(Phase, int, string) {})
			if tc.wantError == "" {
				want := fmt.Sprintf("%x", sha256.Sum256(payload))
				if err != nil || got != want {
					t.Fatalf("checksum=%s err=%v, want=%s", got, err, want)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("err=%v, want %q", err, tc.wantError)
			}
			info, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if observer.read.Load() != tc.wantRead || info.Size() != tc.wantRead {
				t.Fatalf("body read=%d disk bytes=%d, want=%d", observer.read.Load(), info.Size(), tc.wantRead)
			}
			if tc.wantRead == 0 && requests.Load() != 0 {
				t.Fatal("over-budget metadata made a network request")
			}
		})
	}
}

func TestStageTLSBoundariesAndCleanup(t *testing.T) {
	const budget int64 = 1024
	for _, tc := range []struct {
		name string
		size int64
		body int
		code int
		want string
	}{
		{"known-over", 32, 4096, 200, "超过上限"},
		{"unknown-over", 0, 4096, 200, "超过上限"},
		{"short", 128, 64, 200, "下载不完整"},
		{"bad-status", 128, 128, 503, "HTTP 503"},
		{"bad-checksum", 128, 128, 200, "SHA256 不匹配"},
		{"metadata-over", budget + 1, 1, 200, "声明大小"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testPaths(t)
			if err := os.WriteFile(p.Current, []byte("current-canary"), 0o755); err != nil {
				t.Fatal(err)
			}
			name := AssetName("v99.0.0", runtime.GOOS, runtime.GOARCH)
			var requests atomic.Int32
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if strings.HasSuffix(r.URL.Path, sumsAsset) {
					fmt.Fprintf(w, "%s  %s\n", strings.Repeat("0", 64), name)
					return
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write(bytes.Repeat([]byte("x"), tc.body))
			}))
			defer s.Close()
			rel := &Release{TagName: "v99.0.0", Assets: []Asset{
				{Name: name, URL: "https://github.com/" + name, Size: tc.size},
				{Name: sumsAsset, URL: "https://github.com/" + sumsAsset},
			}}
			err := stageAt(t.Context(), localTLSClient(t, s), rel, "1.0.0", nil, p, budget)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
			if readAll(t, p.Current) != "current-canary" {
				t.Fatal("failed stage changed current executable")
			}
			entries, err := os.ReadDir(p.Dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("stage left intermediate files: %v %v", entries, err)
			}
			if tc.name == "metadata-over" && requests.Load() != 0 {
				t.Fatal("metadata rejection must precede checksum request")
			}
		})
	}
}

func TestStagePreservesPendingUpdate(t *testing.T) {
	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			p := testPaths(t)
			if kind == "symlink" {
				target := filepath.Join(p.Dir, "target")
				if err := os.WriteFile(target, []byte("canary"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, p.New); err != nil {
					if runtime.GOOS == "windows" {
						t.Skipf("symlink privilege unavailable: %v", err)
					}
					t.Fatal(err)
				}
			} else if err := os.WriteFile(p.New, []byte("canary"), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{p.Sum, p.Marker} {
				if err := os.WriteFile(path, []byte("previous"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// nil client/release 使任何进入网络阶段的行为立即暴露。
			err := stageAt(t.Context(), nil, nil, "1.0.0", nil, p, maxZIPSize)
			if err == nil || !strings.Contains(err.Error(), "待安装路径") {
				t.Fatalf("pending update not rejected: %v", err)
			}
			if readAll(t, p.New) != "canary" || readAll(t, p.Sum) != "previous" || readAll(t, p.Marker) != "previous" {
				t.Fatal("pending update changed")
			}
		})
	}
}

func TestStageCancellationCleanup(t *testing.T) {
	p := testPaths(t)
	if err := os.WriteFile(p.Current, []byte("current-canary"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	name := AssetName("v99.0.0", runtime.GOOS, runtime.GOARCH)
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, sumsAsset) {
			fmt.Fprintf(w, "%s  %s\n", strings.Repeat("0", 64), name)
			return
		}
		_, _ = w.Write(bytes.Repeat([]byte("x"), 4096))
		w.(http.Flusher).Flush()
		cancel()
	}))
	defer s.Close()
	rel := &Release{TagName: "v99.0.0", Assets: []Asset{
		{Name: name, URL: "https://github.com/" + name, Size: 8192},
		{Name: sumsAsset, URL: "https://github.com/" + sumsAsset},
	}}
	err := stageAt(ctx, localTLSClient(t, s), rel, "1.0.0", nil, p, maxZIPSize)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("canceled download err=%v", err)
	}
	entries, err := os.ReadDir(p.Dir)
	if err != nil || len(entries) != 1 || readAll(t, p.Current) != "current-canary" {
		t.Fatalf("canceled Stage changed installation: %v %v", entries, err)
	}
}

func TestPrivateMetadataReplacement(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "marker.json")
			target := filepath.Join(dir, "target")
			if err := os.WriteFile(target, []byte("canary"), 0o644); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "regular":
				if err := os.WriteFile(path, []byte("previous"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(target, path); err != nil {
					if runtime.GOOS == "windows" {
						t.Skipf("symlink privilege unavailable: %v", err)
					}
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			err := writeMarker(path, marker{From: "1.0.0", To: "2.0.0", Attempts: 1})
			if kind == "directory" {
				if err == nil {
					t.Fatal("metadata rename over directory succeeded")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Lstat(path)
				if err != nil || !info.Mode().IsRegular() {
					t.Fatalf("replacement is not regular: %v %v", info, err)
				}
				if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
					t.Fatalf("metadata mode=%o", info.Mode().Perm())
				}
				m, ok := readMarker(path)
				if !ok || m.Attempts != 1 || m.From != "1.0.0" || m.To != "2.0.0" {
					t.Fatalf("incomplete marker: %+v %v", m, ok)
				}
			}
			if readAll(t, target) != "canary" {
				t.Fatal("metadata write followed symlink")
			}
			matches, err := filepath.Glob(filepath.Join(dir, ".artex-update-meta-*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("metadata intermediates remain: %v %v", matches, err)
			}
		})
	}
}

func TestSmokeEnvironmentPlatformKeys(t *testing.T) {
	parent := []string{"PATH=path", "HOME=home", "TMPDIR=tmpdir", "TEMP=temp", "TMP=tmp", "SystemRoot=system", "USERPROFILE=profile", "path=lower", "systemroot=lower", "SYSTEMROOT=upper", "ARTEX_HOME=secret", "LD_PRELOAD=loader", smokeEnv + "=old"}
	before := append([]string(nil), parent...)
	for _, goos := range []string{"linux", "darwin", "windows"} {
		got := smokeEnvironment(parent, goos)
		want := append([]string(nil), parent[:7]...)
		if goos == "windows" {
			want = append(want, parent[7:10]...)
		}
		want = append(want, smokeEnv+"=1")
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s env=%v want=%v", goos, got, want)
		}
	}
	if !reflect.DeepEqual(parent, before) {
		t.Fatal("parent environment was mutated")
	}
}

func TestPrivateMarkerReadersSeeCompleteJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.json")
	if err := writeMarker(path, marker{From: "1.0.0", To: "2.0.0"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		for {
			select {
			case <-done:
				result <- nil
				return
			default:
			}
			b, err := os.ReadFile(path)
			var m marker
			if err == nil {
				err = json.Unmarshal(b, &m)
			}
			if err != nil || m.From != "1.0.0" || m.To != "2.0.0" {
				result <- fmt.Errorf("reader saw incomplete marker: %q %v", b, err)
				return
			}
		}
	}()
	for i := 0; i < 50; i++ {
		if err := writeMarker(path, marker{From: "1.0.0", To: "2.0.0", Attempts: i}); err != nil {
			close(done)
			<-result
			t.Fatal(err)
		}
	}
	close(done)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
