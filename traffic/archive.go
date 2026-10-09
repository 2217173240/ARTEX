package traffic

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ArchiveSnapshot is the portable traffic subset embedded in one task archive.
// Large content-addressed bodies are copied alongside this manifest rather than
// base64-encoded, preserving deduplication and allowing the tar writer to stream.
type ArchiveSnapshot struct {
	Version   int               `json:"version"`
	Exchanges []ArchiveExchange `json:"exchanges"`
	Blobs     []string          `json:"blobs"`
}

type ArchiveExchange struct {
	ID          string `json:"id"`
	TS          int64  `json:"ts"`
	Host        string `json:"host"`
	Method      string `json:"method"`
	URLTemplate string `json:"url_template"`
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	ReqLen      int    `json:"req_len"`
	RespLen     int    `json:"resp_len"`
	ReqHead     string `json:"req_head"`
	ReqBody     []byte `json:"req_body,omitempty"`
	ReqBlob     string `json:"req_blob,omitempty"`
	RespHead    string `json:"resp_head"`
	RespBody    []byte `json:"resp_body,omitempty"`
	RespBlob    string `json:"resp_blob,omitempty"`
}

// ExportHosts writes an exact-host snapshot to dir. It holds the traffic writer
// lock while reading SQLite and blobs, so each body and its index row come from
// one consistent point in time.
func (t *Traffic) ExportHosts(hosts []string, dir string) (int64, error) {
	if t == nil || len(hosts) == 0 {
		return 0, nil
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o700); err != nil {
		return 0, err
	}
	t.wmu.Lock()
	defer t.wmu.Unlock()
	unique := uniqueArchiveHosts(hosts)
	placeholders := make([]string, len(unique))
	args := make([]any, len(unique))
	for i, host := range unique {
		placeholders[i] = "?"
		args[i] = host
	}
	rows, err := t.db.Query(`SELECT id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path
FROM exchanges WHERE host IN (`+strings.Join(placeholders, ",")+`) ORDER BY ts,id`, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	snapshot := ArchiveSnapshot{Version: 1}
	blobs := map[string]struct{}{}
	for rows.Next() {
		var item ArchiveExchange
		var legacyPath string
		if err := rows.Scan(&item.ID, &item.TS, &item.Host, &item.Method, &item.URLTemplate,
			&item.URL, &item.Status, &item.ContentType, &item.ReqLen, &item.RespLen, &legacyPath); err != nil {
			return 0, err
		}
		var reqBlob, respBlob sql.NullString
		err := t.db.QueryRow(`SELECT req_head,req_body,req_blob,resp_head,resp_body,resp_blob
FROM exchange_bodies WHERE id=?`, item.ID).Scan(&item.ReqHead, &item.ReqBody, &reqBlob, &item.RespHead, &item.RespBody, &respBlob)
		if errors.Is(err, sql.ErrNoRows) && strings.TrimSpace(legacyPath) != "" {
			if !filepath.IsLocal(legacyPath) {
				return 0, fmt.Errorf("invalid legacy traffic path %q", legacyPath)
			}
			req, readErr := readArchiveFile(filepath.Join(t.dir, legacyPath, "request.http"))
			if readErr != nil {
				return 0, readErr
			}
			resp, readErr := readArchiveFile(filepath.Join(t.dir, legacyPath, "response.http"))
			// Old captures without a response have no response body to preserve.
			if readErr != nil && !(os.IsNotExist(readErr) && item.RespLen == 0) {
				return 0, readErr
			}
			item.ReqHead, item.RespHead = string(req), string(resp)
		} else if err != nil {
			return 0, err
		}
		item.ReqBlob, item.RespBlob = reqBlob.String, respBlob.String
		if err := normalizeArchiveExchange(&item); err != nil {
			return 0, err
		}
		for _, hash := range []string{item.ReqBlob, item.RespBlob} {
			if hash != "" {
				blobs[hash] = struct{}{}
			}
		}
		snapshot.Exchanges = append(snapshot.Exchanges, item)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for hash := range blobs {
		source, err := t.blobPath(hash)
		if err != nil {
			return 0, err
		}
		data, err := readArchiveBlob(source, hash)
		if err != nil {
			return 0, err
		}
		if err := os.WriteFile(filepath.Join(dir, "blobs", hash+".bin"), data, 0o600); err != nil {
			return 0, err
		}
		snapshot.Blobs = append(snapshot.Blobs, hash)
	}
	if err := validateArchiveBodies(snapshot, func(hash string) (int, error) {
		data, err := readArchiveBlob(filepath.Join(dir, "blobs", hash+".bin"), hash)
		return len(data), err
	}); err != nil {
		return 0, err
	}
	sort.Strings(snapshot.Blobs)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(dir, "traffic.json"), raw, 0o600); err != nil {
		return 0, err
	}
	return int64(len(snapshot.Exchanges)), nil
}

// ImportArchive imports only missing exchange IDs. Current hot rows always win,
// and repeated restore attempts are safe after a partial external failure.
func (t *Traffic) ImportArchive(dir string) (int64, error) {
	if t == nil {
		return 0, nil
	}
	raw, err := readArchiveFile(filepath.Join(dir, "traffic.json"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var snapshot ArchiveSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return 0, err
	}
	if snapshot.Version != 1 {
		return 0, fmt.Errorf("unsupported traffic archive version %d", snapshot.Version)
	}
	t.wmu.Lock()
	defer t.wmu.Unlock()
	for i := range snapshot.Exchanges {
		if err := normalizeArchiveExchange(&snapshot.Exchanges[i]); err != nil {
			return 0, err
		}
	}
	// Validate the whole package before changing hot blobs or metadata. A retry
	// must never accept a partial file left by an earlier failed restore.
	if err := validateArchiveBodies(snapshot, func(hash string) (int, error) {
		data, err := readArchiveBlob(filepath.Join(dir, "blobs", hash+".bin"), hash)
		return len(data), err
	}); err != nil {
		return 0, err
	}
	for _, hash := range snapshot.Blobs {
		data, err := readArchiveBlob(filepath.Join(dir, "blobs", hash+".bin"), hash)
		if err != nil {
			return 0, err
		}
		destination := filepath.Join(t.dir, "_blobs", "sha256", hash[:2], hash+".bin")
		if err := installArchiveBlob(t.dir, destination, data, hash); err != nil {
			return 0, err
		}
	}
	tx, err := t.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	var imported int64
	for _, item := range snapshot.Exchanges {
		res, err := tx.Exec(`INSERT OR IGNORE INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,'')`, item.ID, item.TS, item.Host, item.Method, item.URLTemplate,
			item.URL, item.Status, item.ContentType, item.ReqLen, item.RespLen)
		if err != nil {
			return imported, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO exchange_bodies(id,req_head,req_body,req_blob,resp_head,resp_body,resp_blob)
VALUES(?,?,?,?,?,?,?)`, item.ID, item.ReqHead, item.ReqBody, nullIfEmpty(item.ReqBlob),
			item.RespHead, item.RespBody, nullIfEmpty(item.RespBlob)); err != nil {
			return imported, err
		}
		for _, hash := range []string{item.ReqBlob, item.RespBlob} {
			if hash != "" {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO blob_refs(hash,exchange_id) VALUES(?,?)`, hash, item.ID); err != nil {
					return imported, err
				}
			}
		}
		if t.fts {
			var rowID int64
			if err := tx.QueryRow(`SELECT rowid FROM exchanges WHERE id=?`, item.ID).Scan(&rowID); err != nil {
				return imported, err
			}
			content := strings.Join([]string{item.URL, item.ReqHead, string(item.ReqBody), item.RespHead, string(item.RespBody)}, "\n")
			if _, err := tx.Exec(`INSERT INTO ex_fts(rowid,content) VALUES(?,?)`, rowID, content); err != nil {
				return imported, err
			}
		}
		imported++
	}
	if err := tx.Commit(); err != nil {
		return imported, err
	}
	return imported, nil
}

func uniqueArchiveHosts(hosts []string) []string {
	seen := make(map[string]struct{}, len(hosts))
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" {
			continue
		}
		if _, exists := seen[host]; exists {
			continue
		}
		seen[host] = struct{}{}
		out = append(out, host)
	}
	sort.Strings(out)
	return out
}

var legacyArchiveBlobRe = regexp.MustCompile(`^@blob sha256:([0-9a-f]{64}) \(len=([0-9]+)\)$`)

// Earlier traffic archives placed the entire legacy .http file in the head
// field. Separate its body, including the old content-addressed placeholder,
// before checking lengths or restoring it to exchange_bodies.
func normalizeArchiveExchange(item *ArchiveExchange) error {
	for _, side := range []struct {
		name   string
		head   *string
		body   *[]byte
		blob   *string
		length int
	}{
		{"request", &item.ReqHead, &item.ReqBody, &item.ReqBlob, item.ReqLen},
		{"response", &item.RespHead, &item.RespBody, &item.RespBlob, item.RespLen},
	} {
		if side.length < 0 {
			return fmt.Errorf("traffic %s %s has negative body length", item.ID, side.name)
		}
		if *side.blob != "" || len(*side.body) != 0 {
			continue
		}
		raw := []byte(*side.head)
		separator, headEnd := bytes.Index(raw, []byte("\n\n")), 1
		if crlf := bytes.Index(raw, []byte("\r\n\r\n")); crlf >= 0 && (separator < 0 || crlf < separator) {
			separator, headEnd = crlf, 2
		}
		if separator < 0 {
			continue
		}
		*side.head = string(raw[:separator+headEnd])
		*side.body = raw[separator+2*headEnd:]
		if marker := legacyArchiveBlobRe.FindSubmatch(*side.body); marker != nil && len(*side.body) != side.length {
			length, err := strconv.Atoi(string(marker[2]))
			if err != nil || length != side.length {
				return fmt.Errorf("traffic %s %s legacy blob length mismatch", item.ID, side.name)
			}
			*side.blob = string(marker[1])
			*side.body = nil
		}
	}
	return nil
}

func validateArchiveBodies(snapshot ArchiveSnapshot, blobSize func(string) (int, error)) error {
	sizes := make(map[string]int, len(snapshot.Blobs))
	for _, hash := range snapshot.Blobs {
		if !blobHashRe.MatchString(hash) {
			return fmt.Errorf("invalid archived traffic blob %q", hash)
		}
		if _, checked := sizes[hash]; checked {
			continue
		}
		size, err := blobSize(hash)
		if err != nil {
			return err
		}
		sizes[hash] = size
	}
	for _, item := range snapshot.Exchanges {
		for _, side := range []struct {
			name   string
			body   []byte
			blob   string
			length int
		}{
			{"request", item.ReqBody, item.ReqBlob, item.ReqLen},
			{"response", item.RespBody, item.RespBlob, item.RespLen},
		} {
			size := len(side.body)
			if side.blob != "" {
				var present bool
				size, present = sizes[side.blob]
				if !present {
					return fmt.Errorf("traffic %s %s references unlisted blob %q", item.ID, side.name, side.blob)
				}
			}
			if size != side.length {
				return fmt.Errorf("traffic %s %s body length mismatch: got %d, want %d", item.ID, side.name, size, side.length)
			}
		}
	}
	return nil
}

func readArchiveFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("traffic archive file is not regular: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("traffic archive file is not regular: %s", path)
	}
	return io.ReadAll(f)
}

func readArchiveBlob(path, hash string) ([]byte, error) {
	data, err := readArchiveFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, fmt.Errorf("traffic blob checksum mismatch: %s", hash)
	}
	return data, nil
}

// A complete, synced temporary file is renamed into the managed store. Both
// capture and archive operations hold wmu, so no row can observe half a body.
func installArchiveBlob(root, destination string, data []byte, hash string) error {
	dir := root
	for _, component := range []string{"_blobs", "sha256", hash[:2]} {
		dir = filepath.Join(dir, component)
		if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("traffic blob directory is not a directory: %s", dir)
		}
	}
	if current, err := readArchiveFile(destination); err == nil {
		if bytes.Equal(current, data) {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(dir, ".restore-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) //nolint:errcheck
	defer f.Close()           //nolint:errcheck
	if err := f.Chmod(0o644); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), destination); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
