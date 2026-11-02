//go:build embedui

package server

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestWebUICachePolicy(t *testing.T) {
	root, err := fs.Sub(webuiDist, "webui/dist")
	if err != nil {
		t.Fatal(err)
	}
	assets := map[string]string{}
	route := ""
	err = fs.WalkDir(root, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(name, "_next/static/") {
			for _, ext := range []string{".js", ".css", ".woff2"} {
				if strings.HasSuffix(name, ext) && assets[ext] == "" {
					assets[ext] = name
				}
			}
		} else if strings.HasSuffix(name, "/index.html") && route == "" {
			route = strings.TrimSuffix(name, "/index.html")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{".js", ".css", ".woff2"} {
		if assets[ext] == "" {
			t.Fatalf("real Next export missing %s asset", ext)
		}
	}
	if route == "" {
		t.Fatal("real Next export missing exported route/index.html")
	}
	srv := httptest.NewServer((&Server{}).webuiHandler())
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	rangeSize := 0
	request := func(t *testing.T, method, target string, headers map[string]string, status int, cache string, want []byte) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != status {
			t.Errorf("status = %d, want %d", resp.StatusCode, status)
		}
		if got := resp.Header.Get("Cache-Control"); got != cache {
			t.Errorf("Cache-Control = %q, want %q", got, cache)
		}
		if method != http.MethodHead && want != nil && !bytes.Equal(body, want) {
			t.Errorf("body differs from embedded file: got %d bytes, want %d", len(body), len(want))
		}
		if method == "HEAD" && len(body) != 0 {
			t.Error("HEAD returned body")
		}
		if method == "HEAD" && resp.Header.Get("Content-Length") != strconv.Itoa(len(want)) {
			t.Error("HEAD Content-Length differs from file")
		}
		if status == 206 && resp.Header.Get("Content-Range") != "bytes 0-0/"+strconv.Itoa(rangeSize) {
			t.Errorf("Content-Range = %q", resp.Header.Get("Content-Range"))
		}
	}
	const immutable = "public, max-age=31536000, immutable"
	for ext, name := range assets {
		data, err := fs.ReadFile(root, name)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(ext+" GET", func(t *testing.T) { request(t, "GET", "/"+name+"?v=anything", nil, 200, immutable, data) })
		t.Run(ext+" HEAD", func(t *testing.T) { request(t, "HEAD", "/"+name, nil, 200, immutable, data) })
	}
	name := assets[".js"]
	data, err := fs.ReadFile(root, name)
	if err != nil {
		t.Fatal(err)
	}
	rangeSize = len(data)
	index, err := fs.ReadFile(root, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	routeHTML, err := fs.ReadFile(root, route+"/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, target string
		headers              map[string]string
		status               int
		cache                string
		body                 []byte
	}{
		{"range", "GET", "/" + name, map[string]string{"Range": "bytes=0-0"}, 206, immutable, data[:1]},
		{"not modified", "GET", "/" + name, map[string]string{"If-None-Match": "*"}, 304, immutable, []byte{}},
		{"invalid range", "GET", "/" + name, map[string]string{"Range": "bytes=invalid"}, 416, "no-cache", nil},
		{"unsatisfiable range", "GET", "/" + name, map[string]string{"Range": "bytes=999999999999-"}, 416, "no-cache", nil},
		{"precondition", "GET", "/" + name, map[string]string{"If-Match": "\"missing\""}, 412, "no-cache", nil},
		{"post", "POST", "/" + name, nil, 200, "no-cache", data},
		{"root", "GET", "/", nil, 200, "no-cache", index},
		{"route", "GET", "/" + route + "/", nil, 200, "no-cache", routeHTML},
		{"index redirect", "GET", "/" + route + "/index.html", nil, 301, "no-cache", nil},
		{"root index redirect", "GET", "/index.html", nil, 301, "no-cache", nil},
		{"spa", "GET", "/r39-unknown-route", nil, 200, "no-cache", index},
		{"static missing", "GET", "/_next/static/r39-missing.js", nil, 200, "no-cache", index},
		{"static html missing", "GET", "/_next/static/r39-missing.html", nil, 200, "no-cache", index},
		{"nested static", "GET", "/prefix/_next/static/r39-missing.js", nil, 200, "no-cache", index},
		{"namespace boundary", "GET", "/_next/staticity/r39-missing.js", nil, 200, "no-cache", index},
		{"dotdot", "GET", "/_next/static/../static/" + strings.TrimPrefix(name, "_next/static/"), nil, 400, "no-cache", nil},
		{"encoded dotdot", "GET", "/_next/static/%2e%2e/static/" + strings.TrimPrefix(name, "_next/static/"), nil, 400, "no-cache", nil},
		{"clean dots", "GET", "/_next/static/./" + strings.TrimPrefix(name, "_next/static/"), nil, 200, immutable, data},
		{"repeated slash", "GET", "/_next//static/" + strings.TrimPrefix(name, "_next/static/"), nil, 200, immutable, data},
	} {
		t.Run(tc.name, func(t *testing.T) { request(t, tc.method, tc.target, tc.headers, tc.status, tc.cache, tc.body) })
	}
}
