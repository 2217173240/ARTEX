package traffic

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBlobRangeIntegrityAndVerifiedCache(t *testing.T) {
	t.Run("corrupted-blob-fails-first-read", func(t *testing.T) {
		tr, _ := openTraffic(t)
		body := bytes.Repeat([]byte("A"), maxInlineBody+4096)
		sum := sha256.Sum256(body)
		hash := hex.EncodeToString(sum[:])

		f := newFlow("corrupt.example", "GET", "/big", nil, body)
		tr.record(f)

		p, err := tr.blobPath(hash)
		if err != nil {
			t.Fatal(err)
		}
		corrupted := bytes.Clone(body)
		corrupted[len(corrupted)-1] ^= 0xff
		if err := os.WriteFile(p, corrupted, 0o644); err != nil {
			t.Fatal(err)
		}

		if _, _, err := tr.BlobRange(hash, 0, 64); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
			t.Fatalf("expected checksum mismatch error on corrupted blob, got %v", err)
		}
		if _, _, err := tr.BlobRange(hash, int64(len(body))+100, 64); err == nil {
			t.Fatal("expected checksum mismatch even when offset >= size on unverified blob")
		}
		tr.verifiedMu.RLock()
		_, cached := tr.verifiedBlobs[hash]
		tr.verifiedMu.RUnlock()
		if cached {
			t.Fatal("corrupted blob must not be cached in verifiedBlobs")
		}
	})

	t.Run("multipage-read-hits-cache-without-rehashing", func(t *testing.T) {
		tr, _ := openTraffic(t)
		var bodyBuf bytes.Buffer
		for i := range 20000 {
			fmt.Fprintf(&bodyBuf, "line-%05d-payload-0123456789\n", i)
		}
		body := bodyBuf.Bytes()
		if len(body) <= maxInlineBody {
			t.Fatalf("test body %d must exceed maxInlineBody %d", len(body), maxInlineBody)
		}
		sum := sha256.Sum256(body)
		hash := hex.EncodeToString(sum[:])

		tr.record(newFlow("pages.example", "GET", "/dump", nil, body))

		const pageSize = 8192
		var reassembled []byte
		pages := 0
		for off := int64(0); off < int64(len(body)); off += pageSize {
			chunk, total, err := tr.BlobRange(hash, off, pageSize)
			if err != nil {
				t.Fatalf("BlobRange(%d) err=%v", off, err)
			}
			if total != int64(len(body)) {
				t.Fatalf("BlobRange(%d) total=%d, want %d", off, total, len(body))
			}
			reassembled = append(reassembled, chunk...)
			pages++
		}
		if pages < 4 {
			t.Fatalf("expected at least 4 pages, got %d", pages)
		}
		if !bytes.Equal(reassembled, body) {
			t.Fatal("reassembled multi-page BlobRange bytes differ from original body")
		}
		if got := tr.blobVerifyCnt.Load(); got != 1 {
			t.Fatalf("expected exactly 1 full SHA256 verification across %d pages, got %d", pages, got)
		}

		// Direct Blob() streaming must still verify full SHA256 every time.
		f, size, err := tr.Blob(hash)
		if err != nil {
			t.Fatalf("Blob() err=%v", err)
		}
		f.Close()
		if size != int64(len(body)) {
			t.Fatalf("Blob() size=%d, want %d", size, len(body))
		}
		if got := tr.blobVerifyCnt.Load(); got != 2 {
			t.Fatalf("expected Blob() to verify again (count=2), got %d", got)
		}
	})

	t.Run("disk-modification-invalidates-cache-and-catches-tampering", func(t *testing.T) {
		tr, _ := openTraffic(t)
		body := bytes.Repeat([]byte("0123456789abcdef"), (maxInlineBody/16)+256)
		sum := sha256.Sum256(body)
		hash := hex.EncodeToString(sum[:])

		tr.record(newFlow("tamper.example", "GET", "/blob", nil, body))

		if _, _, err := tr.BlobRange(hash, 0, 128); err != nil {
			t.Fatalf("initial BlobRange err=%v", err)
		}
		if got := tr.blobVerifyCnt.Load(); got != 1 {
			t.Fatalf("initial verify count=%d, want 1", got)
		}

		p, err := tr.blobPath(hash)
		if err != nil {
			t.Fatal(err)
		}
		origSt, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}

		// Case 1: same size, modified content + changed mtime -> must re-verify and fail.
		tampered := bytes.Clone(body)
		tampered[100] ^= 0xff
		if err := os.WriteFile(p, tampered, 0o644); err != nil {
			t.Fatal(err)
		}
		newTime := origSt.ModTime().Add(2 * time.Second)
		if err := os.Chtimes(p, newTime, newTime); err != nil {
			t.Fatal(err)
		}
		if _, _, err := tr.BlobRange(hash, 0, 128); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
			t.Fatalf("expected checksum mismatch after mtime+content tamper, got %v", err)
		}
		if got := tr.blobVerifyCnt.Load(); got != 2 {
			t.Fatalf("expected re-verification on mtime change (count=2), got %d", got)
		}

		// Restore valid file and re-warm cache.
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, origSt.ModTime(), origSt.ModTime()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := tr.BlobRange(hash, 0, 128); err != nil {
			t.Fatalf("restored BlobRange err=%v", err)
		}
		if got := tr.blobVerifyCnt.Load(); got != 3 {
			t.Fatalf("expected verify count=3 after restore, got %d", got)
		}

		// Case 2: changed size with original mtime preserved -> must re-verify and fail.
		extended := append(bytes.Clone(body), '!')
		if err := os.WriteFile(p, extended, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, origSt.ModTime(), origSt.ModTime()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := tr.BlobRange(hash, 0, 128); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
			t.Fatalf("expected checksum mismatch after size tamper, got %v", err)
		}
		if got := tr.blobVerifyCnt.Load(); got != 4 {
			t.Fatalf("expected re-verification on size change (count=4), got %d", got)
		}
	})

	t.Run("host-deletion-and-cap-clear-verified-cache", func(t *testing.T) {
		tr, _ := openTraffic(t)
		body := bytes.Repeat([]byte("Z"), maxInlineBody+1024)
		sum := sha256.Sum256(body)
		hash := hex.EncodeToString(sum[:])

		tr.record(newFlow("delete-cache.example", "GET", "/z", nil, body))
		if _, _, err := tr.BlobRange(hash, 0, 64); err != nil {
			t.Fatal(err)
		}
		tr.verifiedMu.RLock()
		nBefore := len(tr.verifiedBlobs)
		tr.verifiedMu.RUnlock()
		if nBefore != 1 {
			t.Fatalf("verifiedBlobs len=%d, want 1", nBefore)
		}

		if _, err := tr.DeleteHost("delete-cache.example"); err != nil {
			t.Fatal(err)
		}
		tr.verifiedMu.RLock()
		nAfter := len(tr.verifiedBlobs)
		tr.verifiedMu.RUnlock()
		if nAfter != 0 {
			t.Fatalf("verifiedBlobs len after DeleteHost=%d, want 0", nAfter)
		}

		// Verify cap eviction at maxVerifiedBlobs.
		for i := range maxVerifiedBlobs {
			h := fmt.Sprintf("%064x", i)
			tr.verifiedMu.Lock()
			if tr.verifiedBlobs == nil {
				tr.verifiedBlobs = make(map[string]blobStamp)
			}
			tr.verifiedBlobs[h] = blobStamp{size: int64(i + 1), modNano: int64(i + 1)}
			tr.verifiedMu.Unlock()
		}
		tr.record(newFlow("cap.example", "GET", "/cap", nil, body))
		if _, _, err := tr.BlobRange(hash, 0, 64); err != nil {
			t.Fatal(err)
		}
		tr.verifiedMu.RLock()
		nCap := len(tr.verifiedBlobs)
		tr.verifiedMu.RUnlock()
		if nCap != 1 {
			t.Fatalf("verifiedBlobs len after cap clear=%d, want 1", nCap)
		}
	})
}
