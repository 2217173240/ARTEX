package tool

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestWebFetchBrokenEgressProxyFailsClosed(t *testing.T) {
	var targetHits atomic.Int32
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("should not be reached directly"))
	}))
	defer targetSrv.Close()

	// Closed local proxy listener triggers connection refused (a proxyLikelyErr).
	deadProxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadProxyURL := deadProxy.URL
	deadProxy.Close()

	// Abruptly closing TCP proxy triggers EOF (also a proxyLikelyErr).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	for _, tc := range []struct {
		name  string
		proxy string
	}{
		{"connection refused egress proxy", deadProxyURL},
		{"eof egress proxy", "http://" + ln.Addr().String()},
		{"invalid egress proxy URL", "://bad-proxy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := targetHits.Load()
			wf := NewWebFetch(WebFetchConfig{Proxy: tc.proxy})
			input, _ := json.Marshal(map[string]any{"url": targetSrv.URL + "/probe"})
			res, err := wf.Call(context.Background(), input, nil)
			if err != nil {
				t.Fatalf("Run returned unexpected Go error: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected tool error when egress proxy fails, got: %s", res.Flatten())
			}
			if targetHits.Load() != before {
				t.Fatalf("WebFetch fell back to direct dial despite Proxy=%q (targetHits %d -> %d)",
					tc.proxy, before, targetHits.Load())
			}
		})
	}
}
