package traffic

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureKeepsBodyWhenExistingBlobHasSameLengthCorruption(t *testing.T) {
	tr, dir := openTraffic(t)
	body := bytes.Repeat([]byte("a"), maxInlineBody+1)
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	path := filepath.Join(dir, "_blobs", "sha256", hash[:2], hash+".bin")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("b"), len(body)), 0644); err != nil {
		t.Fatal(err)
	}
	tr.record(newFlow("integrity.example", "POST", "/capture", body, body))
	var req, resp []byte
	var reqBlob, respBlob sql.NullString
	if err := tr.DB().QueryRow(`SELECT req_body,resp_body,req_blob,resp_blob FROM exchange_bodies WHERE id=?`, onlyExchangeID(t, tr)).Scan(&req, &resp, &reqBlob, &respBlob); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(req, body) || !bytes.Equal(resp, body) || reqBlob.Valid || respBlob.Valid {
		t.Fatalf("capture lost intact body: lengths=(%d,%d), blobs=(%v,%v)", len(req), len(resp), reqBlob, respBlob)
	}
}

func TestBlobRejectsCorruptionAndSymlink(t *testing.T) {
	for _, kind := range []string{"corrupt", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			tr, dir := openTraffic(t)
			body := bytes.Repeat([]byte("a"), maxInlineBody+1)
			tr.record(newFlow("integrity.example", "POST", "/capture", body, body))
			sum := sha256.Sum256(body)
			hash := hex.EncodeToString(sum[:])
			path := filepath.Join(dir, "_blobs", "sha256", hash[:2], hash+".bin")
			if kind == "corrupt" {
				if err := os.WriteFile(path, bytes.Repeat([]byte("b"), len(body)), 0644); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "body.bin")
				if err := os.WriteFile(target, body, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if f, _, err := tr.Blob(hash); err == nil {
				f.Close()
				t.Fatal("Blob accepted invalid managed body")
			}
		})
	}
}
