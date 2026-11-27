package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInsertAssetsCapsBody(t *testing.T) {
	s := &Server{m: &Manager{}}

	large := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/assets", strings.NewReader(`{"assets":[{"type":"`+strings.Repeat("a", maxInsertAssetsBodyBytes)+`"}]}`))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=abc")
	s.insertAssets(large, req)
	if large.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s, want 413", large.Code, large.Body.String())
	}

	small := httptest.NewRecorder()
	ok := httptest.NewRequest(http.MethodPost, "/api/assets", strings.NewReader(`{"assets":[]}`))
	ok.Header.Set("Content-Type", "multipart/form-data; boundary=abc")
	s.insertAssets(small, ok)
	if small.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want 503 without a store", small.Code, small.Body.String())
	}
}
