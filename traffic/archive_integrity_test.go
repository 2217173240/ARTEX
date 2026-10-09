package traffic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func archiveTestHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
func writeArchiveFixture(t *testing.T, dir string, items []ArchiveExchange, bodies map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0700); err != nil {
		t.Fatal(err)
	}
	snap := ArchiveSnapshot{Version: 1, Exchanges: items}
	for hash, body := range bodies {
		snap.Blobs = append(snap.Blobs, hash)
		if err := os.WriteFile(filepath.Join(dir, "blobs", hash+".bin"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "traffic.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func insertArchiveBodyFixture(t *testing.T, tr *Traffic, item ArchiveExchange, legacy string) {
	t.Helper()
	if _, err := tr.db.Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path) VALUES(?,1,?,'GET','/','http://fixture.invalid/',200,'',?,?,?)`, item.ID, item.Host, item.ReqLen, item.RespLen, legacy); err != nil {
		t.Fatal(err)
	}
	if legacy == "" {
		if _, err := tr.db.Exec(`INSERT INTO exchange_bodies(id,req_head,req_body,req_blob,resp_head,resp_body,resp_blob) VALUES(?,?,?,?,?,?,?)`, item.ID, item.ReqHead, item.ReqBody, nullIfEmpty(item.ReqBlob), item.RespHead, item.RespBody, nullIfEmpty(item.RespBlob)); err != nil {
			t.Fatal(err)
		}
	}
}
func TestArchiveImportRepairsExistingCorruptBlob(t *testing.T) {
	for _, sameLength := range []bool{false, true} {
		t.Run(strconv.FormatBool(sameLength), func(t *testing.T) {
			tr, _ := openTraffic(t)
			body := []byte("complete archived body")
			hash := archiveTestHash(body)
			dir := t.TempDir()
			writeArchiveFixture(t, dir, []ArchiveExchange{{ID: "retry", Host: "fixture.invalid", ReqLen: len(body), ReqBlob: hash}}, map[string][]byte{hash: body})
			destination := filepath.Join(tr.dir, "_blobs", "sha256", hash[:2], hash+".bin")
			if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
				t.Fatal(err)
			}
			corrupt := body[:3]
			if sameLength {
				corrupt = bytes.Repeat([]byte("x"), len(body))
			}
			if err := os.WriteFile(destination, corrupt, 0600); err != nil {
				t.Fatal(err)
			}
			if n, err := tr.ImportArchive(dir); err != nil || n != 1 {
				t.Fatalf("import=%d, %v", n, err)
			}
			f, _, err := tr.Blob(hash)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(f)
			f.Close()
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("body=%q, %v", got, err)
			}
		})
	}
}
func TestArchiveExportRejectsCorruptBlob(t *testing.T) {
	tr, _ := openTraffic(t)
	body := []byte("original body")
	hash := archiveTestHash(body)
	path := filepath.Join(tr.dir, "_blobs", "sha256", hash[:2], hash+".bin")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	insertArchiveBodyFixture(t, tr, ArchiveExchange{ID: "bad", Host: "fixture.invalid", ReqLen: len(body), ReqBlob: hash}, "")
	dir := t.TempDir()
	if n, err := tr.ExportHosts([]string{"fixture.invalid"}, dir); err == nil || n != 0 {
		t.Fatalf("export=%d, %v; want error", n, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "traffic.json")); !os.IsNotExist(err) {
		t.Fatalf("failed export published manifest: %v", err)
	}
}
func TestArchiveExportRejectsMissingExpectedLegacyResponse(t *testing.T) {
	tr, _ := openTraffic(t)
	rel := filepath.Join("fixture.invalid", "record")
	legacy := filepath.Join(tr.dir, rel)
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "request.http"), []byte("GET / HTTP/1.1\nHost: fixture.invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	insertArchiveBodyFixture(t, tr, ArchiveExchange{ID: "legacy", Host: "fixture.invalid", RespLen: 8}, rel)
	if n, err := tr.ExportHosts([]string{"fixture.invalid"}, t.TempDir()); err == nil || n != 0 {
		t.Fatalf("export=%d, %v; want missing response error", n, err)
	}
}

func TestArchiveRejectsIncompleteBodiesBeforeInstalling(t *testing.T) {
	for _, tc := range []struct {
		name   string
		item   ArchiveExchange
		blob   []byte
		listed bool
	}{
		{"inline-length", ArchiveExchange{ReqLen: 4, ReqBody: []byte("bad")}, nil, false},
		{"blob-length", ArchiveExchange{ReqLen: 10}, []byte("body"), true},
		{"unlisted-blob", ArchiveExchange{ReqLen: 4}, []byte("body"), false},
		{"negative-length", ArchiveExchange{ReqLen: -1}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, _ := openTraffic(t)
			dir := t.TempDir()
			item := tc.item
			item.ID = "invalid"
			item.Host = "fixture.invalid"
			good := []byte("verified other blob")
			goodHash := archiveTestHash(good)
			bodies := map[string][]byte{goodHash: good}
			if tc.blob != nil {
				item.ReqBlob = archiveTestHash(tc.blob)
				if tc.listed {
					bodies[item.ReqBlob] = tc.blob
				}
			}
			writeArchiveFixture(t, dir, []ArchiveExchange{item}, bodies)
			if n, err := tr.ImportArchive(dir); err == nil || n != 0 {
				t.Fatalf("import=%d, %v; want error", n, err)
			}
			var count int
			if err := tr.db.QueryRow(`SELECT count(*) FROM exchanges`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("rows=%d, %v", count, err)
			}
			if _, err := os.Stat(filepath.Join(tr.dir, "_blobs", "sha256", goodHash[:2], goodHash+".bin")); !os.IsNotExist(err) {
				t.Fatalf("invalid package installed a blob: %v", err)
			}
		})
	}
}

func TestArchiveRequiresRegularBodyFiles(t *testing.T) {
	for _, kind := range []string{"directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			tr, _ := openTraffic(t)
			dir := t.TempDir()
			body := []byte("body")
			hash := archiveTestHash(body)
			writeArchiveFixture(t, dir, []ArchiveExchange{{ID: "bad", ReqLen: len(body), ReqBlob: hash}}, map[string][]byte{hash: body})
			source := filepath.Join(dir, "blobs", hash+".bin")
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			if kind == "directory" {
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				target := filepath.Join(t.TempDir(), "body")
				if err := os.WriteFile(target, body, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, source); err != nil {
					t.Fatal(err)
				}
			}
			if n, err := tr.ImportArchive(dir); err == nil || n != 0 {
				t.Fatalf("import=%d, %v; want regular-file error", n, err)
			}
		})
	}
}

func TestArchiveImportRejectsSymlinkedManagedDirectory(t *testing.T) {
	tr, _ := openTraffic(t)
	body := []byte("body")
	hash := archiveTestHash(body)
	dir := t.TempDir()
	outside := t.TempDir()
	writeArchiveFixture(t, dir, []ArchiveExchange{{ID: "link", ReqLen: len(body), ReqBlob: hash}}, map[string][]byte{hash: body})
	if err := os.MkdirAll(filepath.Join(tr.dir, "_blobs", "sha256"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tr.dir, "_blobs", "sha256", hash[:2])); err != nil {
		t.Fatal(err)
	}
	if n, err := tr.ImportArchive(dir); err == nil || n != 0 {
		t.Fatalf("import=%d, %v; want managed-directory error", n, err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("external directory changed: %v, %v", entries, err)
	}
}

func TestArchiveLegacyAndEmptyBodiesRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name       string
		req, resp  string
		body       []byte
		noResponse bool
	}{
		{name: "inline", req: "POST / HTTP/1.1\nHost: fixture.invalid\n\nrequest", resp: "HTTP 200\r\n\r\nresponse", body: []byte("response")},
		{name: "empty", req: "GET / HTTP/1.1\nHost: fixture.invalid", noResponse: true},
		{name: "old-bucket-blob", req: "GET / HTTP/1.1\nHost: fixture.invalid", body: bytes.Repeat([]byte("b"), maxInlineBody+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, _ := openTraffic(t)
			rel := filepath.Join("fixture.invalid", "record")
			legacy := filepath.Join(tr.dir, rel)
			if err := os.MkdirAll(legacy, 0700); err != nil {
				t.Fatal(err)
			}
			item := ArchiveExchange{ID: "legacy", Host: "fixture.invalid", RespLen: len(tc.body)}
			if tc.name == "inline" {
				item.ReqLen = len("request")
			}
			resp := tc.resp
			if tc.name == "old-bucket-blob" {
				hash := archiveTestHash(tc.body)
				path := filepath.Join(tr.dir, "_blobs", "sha256", hash[:2], hash[2:4], hash+".bin")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, tc.body, 0600); err != nil {
					t.Fatal(err)
				}
				resp = "HTTP 200\n\n@blob sha256:" + hash + " (len=" + strconv.Itoa(len(tc.body)) + ")"
			}
			if err := os.WriteFile(filepath.Join(legacy, "request.http"), []byte(tc.req), 0600); err != nil {
				t.Fatal(err)
			}
			if !tc.noResponse {
				if err := os.WriteFile(filepath.Join(legacy, "response.http"), []byte(resp), 0600); err != nil {
					t.Fatal(err)
				}
			}
			insertArchiveBodyFixture(t, tr, item, rel)
			dir := t.TempDir()
			if n, err := tr.ExportHosts([]string{item.Host}, dir); err != nil || n != 1 {
				t.Fatalf("export=%d, %v", n, err)
			}
			dst, _ := openTraffic(t)
			if n, err := dst.ImportArchive(dir); err != nil || n != 1 {
				t.Fatalf("import=%d, %v", n, err)
			}
			var got []byte
			if err := dst.ReadEvidence(context.Background(), []string{item.ID}, func(e EvidenceExchange) error { var err error; got, err = io.ReadAll(e.Response); return err }); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.body) {
				t.Fatalf("body length=%d, want %d", len(got), len(tc.body))
			}
		})
	}
}

func TestArchiveImportAcceptsOldEmbeddedHTTPBody(t *testing.T) {
	tr, _ := openTraffic(t)
	dir := t.TempDir()
	body := []byte("legacy body")
	writeArchiveFixture(t, dir, []ArchiveExchange{{ID: "embedded", Host: "fixture.invalid", RespLen: len(body), RespHead: "HTTP 200\n\n" + string(body)}}, nil)
	if n, err := tr.ImportArchive(dir); err != nil || n != 1 {
		t.Fatalf("import=%d, %v", n, err)
	}
	if err := tr.ReadEvidence(context.Background(), []string{"embedded"}, func(e EvidenceExchange) error {
		got, err := io.ReadAll(e.Response)
		if !bytes.Equal(got, body) {
			t.Fatalf("body=%q", got)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveImportSafeRetryAfterMetadataFailure(t *testing.T) {
	tr, _ := openTraffic(t)
	body := []byte("complete archived body")
	hash := archiveTestHash(body)
	dir := t.TempDir()
	writeArchiveFixture(t, dir, []ArchiveExchange{{ID: "retry", Host: "fixture.invalid", ReqLen: len(body), ReqBlob: hash}}, map[string][]byte{hash: body})
	if _, err := tr.db.Exec(`CREATE TRIGGER fail_archive_restore BEFORE INSERT ON exchange_bodies BEGIN SELECT RAISE(ABORT, 'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.ImportArchive(dir); err == nil {
		t.Fatal("expected metadata error")
	}
	var count int
	if err := tr.db.QueryRow(`SELECT count(*) FROM exchanges`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rows=%d, %v", count, err)
	}
	f, _, err := tr.Blob(hash)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	f.Close()
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("body after failed metadata=%q, %v", got, err)
	}
	if _, err := tr.db.Exec(`DROP TRIGGER fail_archive_restore`); err != nil {
		t.Fatal(err)
	}
	if n, err := tr.ImportArchive(dir); err != nil || n != 1 {
		t.Fatalf("retry=%d, %v", n, err)
	}
	if n, err := tr.ImportArchive(dir); err != nil || n != 0 {
		t.Fatalf("repeated retry=%d, %v", n, err)
	}
	files, err := filepath.Glob(filepath.Join(tr.dir, "_blobs", "sha256", hash[:2], ".restore-*.tmp"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary install files remain: %v, %v", files, err)
	}
}

func TestArchiveImportConcurrentWithCapture(t *testing.T) {
	tr, _ := openTraffic(t)
	body := bytes.Repeat([]byte("b"), maxInlineBody+1)
	hash := archiveTestHash(body)
	dir := t.TempDir()
	writeArchiveFixture(t, dir, []ArchiveExchange{{ID: "concurrent", Host: "fixture.invalid", ReqLen: len(body), ReqBlob: hash}}, map[string][]byte{hash: body})
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		for i := 0; i < 10; i++ {
			if _, err := tr.ImportArchive(dir); err != nil {
				errs <- err
				return
			}
		}
		errs <- nil
	}()
	go func() {
		<-start
		for i := 0; i < 10; i++ {
			tr.record(newFlow("fixture.invalid", "POST", "/capture", body, body))
		}
		errs <- nil
	}()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := tr.db.QueryRow(`SELECT count(*) FROM exchanges`).Scan(&count); err != nil || count != 11 {
		t.Fatalf("rows=%d, %v; want 11", count, err)
	}
	f, _, err := tr.Blob(hash)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	f.Close()
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("concurrent body length=%d, %v", len(got), err)
	}
}
