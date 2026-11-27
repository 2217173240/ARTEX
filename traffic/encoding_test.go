package traffic

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
)

const compressedPage = "<html><body>Welcome admin. root:x:0:0:root:/root:/bin/bash</body></html>"

// TestCompressedResponseIsSearchable records a gzip response through the real
// proxy, as a Go client fetches it: Accept-Encoding gzip is sent by default and
// the proxy keeps the body as it crossed the wire, while FTS indexes the decoded
// text so body search finds it.
func TestCompressedResponseIsSearchable(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			io.WriteString(w, compressedPage)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		io.WriteString(gz, compressedPage)
		gz.Close()
	}))
	defer target.Close()

	tr, _ := startLoopbackTraffic(t)
	pu, _ := url.Parse(tr.ProxyAddr())
	transport := &http.Transport{Proxy: http.ProxyURL(pu)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	resp, err := client.Get(target.URL + "/passwd")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	host := strings.TrimPrefix(target.URL, "http://")
	var rows []ExchangeMeta
	for deadline := time.Now().Add(5 * time.Second); len(rows) == 0 && time.Now().Before(deadline); {
		rows, _ = tr.query(host, "", "", 0, 10)
		time.Sleep(10 * time.Millisecond)
	}
	if len(rows) != 1 {
		t.Fatalf("exchange not recorded: %d rows", len(rows))
	}
	if _, r, _ := tr.Get(rows[0].ID); !strings.Contains(r, "Content-Encoding: gzip") {
		t.Fatalf("test needs a gzip capture, got response %q", r)
	}

	hits, err := tr.query(host, "", "root:x:0", 0, 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("body search for text of a gzip response: %d rows, err=%v; want 1", len(hits), err)
	}
}

func TestCompressedBodyIndexEncodings(t *testing.T) {
	var gz, br, zl, rawFlate bytes.Buffer
	gw := gzip.NewWriter(&gz)
	io.WriteString(gw, compressedPage)
	gw.Close()
	bw := brotli.NewWriter(&br)
	io.WriteString(bw, compressedPage)
	bw.Close()
	zw := zlib.NewWriter(&zl)
	io.WriteString(zw, compressedPage)
	zw.Close()
	fw, _ := flate.NewWriter(&rawFlate, flate.DefaultCompression)
	io.WriteString(fw, compressedPage)
	fw.Close()

	for _, tc := range []struct {
		name string
		enc  string
		body []byte
	}{
		{"gzip", "gzip", gz.Bytes()},
		{"x-gzip", "x-gzip", gz.Bytes()},
		{"br", "br", br.Bytes()},
		{"deflate-zlib", "deflate", zl.Bytes()},
		{"deflate-raw", "deflate", rawFlate.Bytes()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, _ := openTraffic(t)
			f := newFlow("api.example.com", "GET", "/"+tc.name, nil, tc.body)
			f.Response.Header.Set("Content-Type", "text/html")
			f.Response.Header.Set("Content-Encoding", tc.enc)
			tr.record(f)
			hits, err := tr.query("api.example.com", "", "root:x:0", 0, 10)
			if err != nil || len(hits) != 1 {
				t.Fatalf("body search for text of a %s response: %d rows, err=%v; want 1", tc.name, len(hits), err)
			}
		})
	}

	t.Run("compressed-request", func(t *testing.T) {
		tr, _ := openTraffic(t)
		f := newFlow("api.example.com", "POST", "/req", gz.Bytes(), []byte("ok"))
		f.Request.Header.Set("Content-Type", "text/plain")
		f.Request.Header.Set("Content-Encoding", "gzip")
		tr.record(f)
		hits, err := tr.query("api.example.com", "", "root:x:0", 0, 10)
		if err != nil || len(hits) != 1 {
			t.Fatalf("body search for text of a gzip request: %d rows, err=%v; want 1", len(hits), err)
		}
	})
}

// TestDecodeBodyIsBounded keeps a compression bomb from a target from being
// expanded beyond the index limit.
func TestDecodeBodyIsBounded(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write(make([]byte, 4*maxIndexBody))
	gz.Close()
	out, ok := decodeBody(http.Header{"Content-Encoding": {"gzip"}}, buf.Bytes(), maxIndexBody)
	if !ok || len(out) != maxIndexBody {
		t.Fatalf("decodeBody returned %d bytes, ok=%v; want %d", len(out), ok, maxIndexBody)
	}
}

// TestTruncatedCompressedBodyIsIndexed covers a capture cut short mid-stream:
// the text decoded before the cut is still indexed.
func TestTruncatedCompressedBodyIsIndexed(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	io.WriteString(gz, compressedPage)
	for i := range 4096 {
		fmt.Fprintf(gz, "row %d\n", i)
	}
	gz.Close()
	cut := buf.Bytes()[:buf.Len()/2]

	tr, _ := openTraffic(t)
	f := newFlow("api.example.com", "GET", "/cut", nil, cut)
	f.Response.Header.Set("Content-Type", "text/html")
	f.Response.Header.Set("Content-Encoding", "gzip")
	tr.record(f)
	hits, err := tr.query("api.example.com", "", "root:x:0", 0, 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("body search for text of a truncated gzip response: %d rows, err=%v; want 1", len(hits), err)
	}
}

// TestIndexBodySkipsBinaryContentType keeps an encoded body with a binary
// content type out of the index without decoding it.
func TestIndexBodySkipsBinaryContentType(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	io.WriteString(gz, compressedPage)
	gz.Close()
	h := http.Header{"Content-Encoding": {"gzip"}, "Content-Type": {"image/png"}}
	if text, ok := indexBody(h, buf.Bytes()); ok || text != "" {
		t.Fatalf("indexBody on an image/png body = (%q, %v), want (\"\", false)", text, ok)
	}
}
