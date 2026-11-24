package server

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"
)

// Workspace file manager — browse / view / edit / download / upload / delete the
// shared work dir (s.m.dir), where all agents write their artifacts. Every path is
// accessed through os.Root (including symlink resolution); this is a filesystem
// boundary for these routes, not a sandbox for agent processes. All routes sit
// behind requireAuth (see Handler()).

const (
	maxWorkspaceRead   = 2 << 20   // 2 MiB: files bigger than this aren't inlined for view/edit (download instead)
	maxWorkspaceUpload = 512 << 20 // 512 MiB per upload request
)

// wsResolve preserves the API's rooted path normalization and returns a name for
// os.Root. Lexical normalization alone does not confine filesystem operations.
func (s *Server) wsResolve(rel string) (string, bool) {
	rel = strings.TrimPrefix(strings.TrimSpace(rel), "/")
	clean := filepath.Clean("/" + rel) // e.g. "/a/../../etc" → "/etc"
	name, err := filepath.Rel(string(os.PathSeparator), clean)
	if err != nil || !filepath.IsLocal(name) {
		return "", false
	}
	return name, true
}

// wsRel renders a rooted name as an API path (forward slashes, empty at root).
func (s *Server) wsRel(name string) string {
	if name == "." {
		return ""
	}
	return filepath.ToSlash(name)
}

// wsOpen pins the workspace directory for the entire request, so later path
// lookups do not reopen it by name if it is renamed or replaced.
func (s *Server) wsOpen(w http.ResponseWriter, rel string) (*workspaceRoot, string) {
	name, ok := s.wsResolve(rel)
	if !ok {
		writeErr(w, 400, "非法路径")
		return nil, ""
	}
	root, err := os.OpenRoot(s.m.dir)
	if err != nil {
		writeErr(w, 500, "工作区不可用："+err.Error())
		return nil, ""
	}
	guard := &workspaceRoot{handle: root}
	if _, err := guard.resolve(name, false); err != nil {
		root.Close()
		writeErr(w, 403, "工作区路径不可访问")
		return nil, ""
	}
	return guard, name
}

// workspaceRoot protects internal storage from ordinary file-manager operations.
// Resolution uses the already opened root and operations use the resolved name,
// preserving stable workspace aliases. A process with filesystem write access can
// still replace directories concurrently; this is not a process sandbox.
type workspaceRoot struct{ handle *os.Root }

func (root *workspaceRoot) Close() error { return root.handle.Close() }

func workspaceReserved(name string, ancestors bool) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(name)), "/")
	for i := range parts {
		if runtime.GOOS == "windows" {
			parts[i] = strings.TrimRight(parts[i], ". ")
		}
		parts[i] = strings.ToLower(parts[i])
	}
	name = strings.Join(parts, "/")
	for _, reserved := range []string{"traffic/_ca", "traffic/_index"} {
		if name == reserved || strings.HasPrefix(name, reserved+"/") ||
			(ancestors && (name == "." || strings.HasPrefix(reserved, name+"/"))) {
			return true
		}
	}
	return false
}

func (root *workspaceRoot) resolve(name string, ancestors bool) (string, error) {
	resolved, err := root.resolvePath(name, true)
	if err != nil {
		return "", err
	}
	if workspaceReserved(resolved, ancestors) {
		return "", os.ErrPermission
	}
	if ancestors {
		// traffic itself may be a legitimate workspace symlink. Protect the
		// backing directories' parents as well as their public path names.
		for _, reserved := range []string{"traffic/_ca", "traffic/_index"} {
			internal, err := root.resolvePath(filepath.FromSlash(reserved), false)
			if err != nil {
				// An unavailable or out-of-root internal target cannot be
				// removed through this root; keep ordinary workspace files usable.
				continue
			}
			rel, err := filepath.Rel(resolved, internal)
			if err == nil && filepath.IsLocal(rel) {
				return "", os.ErrPermission
			}
			// Compare ancestor identity as well: the requested spelling can use
			// native case or short-name aliases which filepath.Rel cannot match.
			requested, statErr := root.handle.Stat(resolved)
			if statErr == nil {
				for parent := internal; ; parent = filepath.Dir(parent) {
					info, err := root.handle.Stat(parent)
					if err == nil && os.SameFile(requested, info) {
						return "", os.ErrPermission
					}
					if parent == "." {
						break
					}
				}
			}
		}
	}
	return resolved, nil
}

func (root *workspaceRoot) resolvePath(name string, protect bool) (string, error) {
	pending := strings.Split(filepath.ToSlash(name), "/")
	resolved := "."
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		candidate := filepath.Join(resolved, part)
		if !filepath.IsLocal(candidate) || (protect && workspaceReserved(candidate, false)) {
			return "", os.ErrPermission
		}
		info, err := root.handle.Lstat(candidate)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return "", os.ErrPermission
			}
			target, err := root.handle.Readlink(candidate)
			if err != nil || !filepath.IsLocal(filepath.Join(resolved, target)) || filepath.IsAbs(target) {
				return "", os.ErrPermission
			}
			pending = append(strings.Split(filepath.ToSlash(target), "/"), pending...)
			continue
		}
		// Filesystem identity also catches platform aliases such as Windows short
		// directory names, without assuming a particular case-folding filesystem.
		if protect && err == nil && info.IsDir() {
			for _, reserved := range []string{"traffic/_ca", "traffic/_index"} {
				internal, statErr := root.handle.Stat(filepath.FromSlash(reserved))
				if statErr == nil && os.SameFile(info, internal) {
					return "", os.ErrPermission
				}
			}
		}
		resolved = candidate
	}
	return resolved, nil
}

func (root *workspaceRoot) Open(name string) (*os.File, error) {
	resolved, err := root.resolve(name, false)
	if err != nil {
		return nil, err
	}
	return root.handle.Open(resolved)
}

func (root *workspaceRoot) Stat(name string) (os.FileInfo, error) {
	resolved, err := root.resolve(name, false)
	if err != nil {
		return nil, err
	}
	return root.handle.Stat(resolved)
}

func (root *workspaceRoot) Lstat(name string) (os.FileInfo, error) {
	if _, err := root.resolve(name, false); err != nil {
		return nil, err
	}
	return root.handle.Lstat(name)
}

func (root *workspaceRoot) MkdirAll(name string, perm os.FileMode) error {
	resolved, err := root.resolve(name, false)
	if err != nil {
		return err
	}
	return root.handle.MkdirAll(resolved, perm)
}

func (root *workspaceRoot) WriteFile(name string, data []byte, perm os.FileMode) error {
	resolved, err := root.resolve(name, false)
	if err != nil {
		return err
	}
	return root.handle.WriteFile(resolved, data, perm)
}

func (root *workspaceRoot) Create(name string) (*os.File, error) {
	resolved, err := root.resolve(name, false)
	if err != nil {
		return nil, err
	}
	return root.handle.Create(resolved)
}

func (root *workspaceRoot) RemoveAll(name string) error {
	if _, err := root.resolve(name, true); err != nil {
		return err
	}
	// Resolve only the parent for removal: deleting a legitimate symlink must
	// unlink the alias, preserving its target and the target's contents.
	parent, err := root.resolve(filepath.Dir(name), false)
	if err != nil {
		return err
	}
	return root.handle.RemoveAll(filepath.Join(parent, filepath.Base(name)))
}

type wsEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // unix millis
}

// GET /api/workspace/list?path=<rel>
func (s *Server) wsList(w http.ResponseWriter, r *http.Request) {
	root, name := s.wsOpen(w, r.URL.Query().Get("path"))
	if root == nil {
		return
	}
	defer root.Close()
	dir, err := root.Open(name)
	if err != nil {
		writeErr(w, 404, "路径不存在")
		return
	}
	defer dir.Close()
	fi, err := dir.Stat()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !fi.IsDir() {
		writeErr(w, 400, "不是目录")
		return
	}
	ents, err := dir.ReadDir(-1)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]wsEntry, 0, len(ents))
	for _, e := range ents {
		entryName := filepath.Join(name, e.Name())
		info, err := root.Lstat(entryName)
		if err != nil {
			continue
		}
		out = append(out, wsEntry{
			Name:  e.Name(),
			Path:  s.wsRel(entryName),
			Dir:   e.IsDir(),
			Size:  info.Size(),
			MTime: info.ModTime().UnixMilli(),
		})
	}
	// 目录在前，各自按名称排序。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	writeJSON(w, 200, map[string]any{"path": s.wsRel(name), "entries": out})
}

// GET /api/workspace/read?path=<rel> — inline text for view/edit. Binary or oversize
// files return {binary:true}/{too_large:true} with no content (use download instead).
func (s *Server) wsRead(w http.ResponseWriter, r *http.Request) {
	root, name := s.wsOpen(w, r.URL.Query().Get("path"))
	if root == nil {
		return
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		writeErr(w, 404, "文件不存在")
		return
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if fi.IsDir() {
		writeErr(w, 400, "是目录，不能作为文件读取")
		return
	}
	if fi.Size() > maxWorkspaceRead {
		writeJSON(w, 200, map[string]any{"path": s.wsRel(name), "size": fi.Size(), "too_large": true, "binary": true})
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, maxWorkspaceRead+1))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if len(data) > maxWorkspaceRead {
		writeJSON(w, 200, map[string]any{"path": s.wsRel(name), "size": len(data), "too_large": true, "binary": true})
		return
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		writeJSON(w, 200, map[string]any{"path": s.wsRel(name), "size": fi.Size(), "binary": true})
		return
	}
	writeJSON(w, 200, map[string]any{"path": s.wsRel(name), "size": fi.Size(), "binary": false, "content": string(data)})
}

// POST /api/workspace/write  {path, content} — create/overwrite a text file.
func (s *Server) wsWrite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	root, name := s.wsOpen(w, req.Path)
	if root == nil {
		return
	}
	defer root.Close()
	if name == "." {
		writeErr(w, 400, "非法路径")
		return
	}
	if fi, err := root.Stat(name); err == nil && fi.IsDir() {
		writeErr(w, 400, "目标是目录")
		return
	}
	if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := root.WriteFile(name, []byte(req.Content), 0o644); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": s.wsRel(name)})
}

// POST /api/workspace/mkdir  {path}
func (s *Server) wsMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	root, name := s.wsOpen(w, req.Path)
	if root == nil {
		return
	}
	defer root.Close()
	if name == "." {
		writeErr(w, 400, "非法路径")
		return
	}
	if err := root.MkdirAll(name, 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": s.wsRel(name)})
}

// DELETE /api/workspace/delete?path=<rel> — removes a file or a directory tree
// (confined to the work dir; the root itself can't be deleted).
func (s *Server) wsDelete(w http.ResponseWriter, r *http.Request) {
	root, name := s.wsOpen(w, r.URL.Query().Get("path"))
	if root == nil {
		return
	}
	defer root.Close()
	if name == "." {
		writeErr(w, 400, "不能删除工作区根目录")
		return
	}
	if _, err := root.resolve(name, true); err != nil {
		writeErr(w, 403, "工作区路径不可删除")
		return
	}
	if _, err := root.Stat(name); err != nil {
		writeErr(w, 404, "路径不存在")
		return
	}
	if err := root.RemoveAll(name); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// GET /api/workspace/download?path=<rel> — stream a file as an attachment.
func (s *Server) wsDownload(w http.ResponseWriter, r *http.Request) {
	root, name := s.wsOpen(w, r.URL.Query().Get("path"))
	if root == nil {
		return
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		writeErr(w, 404, "文件不存在")
		return
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil || fi.IsDir() {
		writeErr(w, 404, "文件不存在")
		return
	}
	filename := filepath.Base(name)
	// RFC 5987 filename* keeps non-ASCII names intact; plain filename is the fallback.
	w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(filename)+"\"; filename*=UTF-8''"+url.PathEscape(filename))
	http.ServeContent(w, r, filename, fi.ModTime(), file)
}

// POST /api/workspace/upload?path=<dir> — multipart form field "file" (one or more).
func (s *Server) wsUpload(w http.ResponseWriter, r *http.Request) {
	root, dirName := s.wsOpen(w, r.URL.Query().Get("path"))
	if root == nil {
		return
	}
	defer root.Close()
	if fi, err := root.Stat(dirName); err != nil || !fi.IsDir() {
		writeErr(w, 400, "目标目录不存在")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkspaceUpload)
	defer r.Body.Close()
	err := r.ParseMultipartForm(32 << 20)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err != nil {
		writeErr(w, 400, "解析上传失败或超出大小限制："+err.Error())
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		writeErr(w, 400, "缺少上传文件(表单字段 file)")
		return
	}
	saved := 0
	// Validate every final filename before saving any part of this request.
	for _, hdr := range files {
		name := filepath.Base(hdr.Filename)
		if name == "" || name == "." || name == ".." {
			continue
		}
		if _, err := root.resolve(filepath.Join(dirName, name), false); err != nil {
			writeErr(w, 403, "工作区路径不可访问")
			return
		}
	}
	for _, hdr := range files {
		name := filepath.Base(hdr.Filename) // strip any path component
		if name == "" || name == "." || name == ".." {
			continue
		}
		dest := filepath.Join(dirName, name)
		if err := saveUpload(hdr, func() (*os.File, error) { return root.Create(dest) }); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		saved++
	}
	writeJSON(w, 200, map[string]any{"uploaded": saved})
}

func saveUpload(hdr *multipart.FileHeader, create func() (*os.File, error)) error {
	src, err := hdr.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := create()
	if err != nil {
		return err
	}
	_, err = io.Copy(out, src)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// sanitizeFilename strips characters unsafe for a Content-Disposition filename token.
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\"", "")
	name = strings.ReplaceAll(name, "\\", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, "\r", "")
	return name
}
