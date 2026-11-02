//go:build embedui

package server

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// webuiDist holds the statically-exported frontend (built with
// `cd web && npm run build:static`, then copied into server/webui/dist). Compiled
// into the binary only when building with `-tags embedui`. The `all:` prefix is
// required so Next's `_next/` asset dir (leading underscore) is included.
//
//go:embed all:webui/dist
var webuiDist embed.FS

// webuiHandler serves the embedded SPA. Public (no JWT) — auth is enforced
// client-side and on /api. Serves the exported per-route index.html files and
// falls back to index.html so client-side routing still resolves unknown paths.
func (s *Server) webuiHandler() http.Handler {
	root, err := fs.Sub(webuiDist, "webui/dist")
	if err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cacheWriter := &webuiCacheWriter{ResponseWriter: w}
		w = cacheWriter
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		// 1) exact file (assets: _next/*, favicon.ico, ...)
		// 2) route dir → <p>/index.html (trailingSlash export)
		// 3) <p>.html
		// 4) SPA 兜底 → index.html（交给客户端路由）
		for _, cand := range []string{p, p + "/index.html", p + ".html"} {
			cacheWriter.immutable = cand == p &&
				(r.Method == http.MethodGet || r.Method == http.MethodHead) &&
				strings.HasPrefix(cand, "_next/static/") &&
				!strings.HasPrefix(cand, "_next/static/service-worker/") &&
				!strings.EqualFold(path.Ext(cand), ".html")
			if serveFileIfExists(w, r, root, cand) {
				return
			}
		}
		cacheWriter.immutable = false
		http.ServeFileFS(w, r, root, "index.html")
	})
}

// webuiCacheWriter 在状态提交时设置缓存策略，覆盖 ServeContent 错误处理对标头的清理。
type webuiCacheWriter struct {
	http.ResponseWriter
	immutable bool
	committed bool
}

func (w *webuiCacheWriter) WriteHeader(status int) {
	if w.committed {
		return
	}
	cache := "no-cache"
	if w.immutable && (status == http.StatusOK || status == http.StatusPartialContent || status == http.StatusNotModified) {
		cache = "public, max-age=31536000, immutable"
	}
	w.Header().Set("Cache-Control", cache)
	w.ResponseWriter.WriteHeader(status)
	if status >= 200 {
		w.committed = true
	}
}

func (w *webuiCacheWriter) Write(p []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (w *webuiCacheWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// serveFileIfExists serves name from fsys when it exists as a regular file.
func serveFileIfExists(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) bool {
	f, err := fsys.Open(name)
	if err != nil {
		return false
	}
	st, statErr := f.Stat()
	_ = f.Close()
	if statErr != nil || st.IsDir() {
		return false
	}
	http.ServeFileFS(w, r, fsys, name)
	return true
}
