package server

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/golang-jwt/jwt/v5"
)

func TestVerifyJWTRequiresHS256(t *testing.T) {
	key := []byte("auth-controls-test-signing-key-32bytes")
	for _, tc := range []struct {
		name   string
		method jwt.SigningMethod
		key    any
		expiry time.Time
		valid  bool
	}{
		{"HS256", jwt.SigningMethodHS256, key, time.Now().Add(time.Hour), true},
		{"HS384 same key", jwt.SigningMethodHS384, key, time.Now().Add(time.Hour), false},
		{"HS512 same key", jwt.SigningMethodHS512, key, time.Now().Add(time.Hour), false},
		{"expired", jwt.SigningMethodHS256, key, time.Now().Add(-time.Hour), false},
		{"wrong key", jwt.SigningMethodHS256, []byte("another-signing-key"), time.Now().Add(time.Hour), false},
		{"none", jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, time.Now().Add(time.Hour), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, err := jwt.NewWithClaims(tc.method, jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(tc.expiry),
			}).SignedString(tc.key)
			if err != nil {
				t.Fatal(err)
			}
			if got := verifyJWT(token, key); got != tc.valid {
				t.Fatalf("verifyJWT = %v, want %v", got, tc.valid)
			}
		})
	}
}

// The pool is closed before any request, so these HTTP tests exercise the real
// handlers without contacting PostgreSQL or running password hashing.
func newAuthControlsServer(t *testing.T) *Server {
	t.Helper()
	conn, err := sql.Open("pgx", "postgres://invalid@127.0.0.1:1/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	return &Server{m: &Manager{pg: &db.DB{DB: conn}}}
}

func authControlsRequest(s *Server, path, remoteAddr, forwardedIP string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"username":"ARTEX","password":"test-password"}`))
	r.RemoteAddr = remoteAddr
	r.Header.Set("X-Forwarded-For", forwardedIP)
	r.Header.Set("Forwarded", "for="+forwardedIP)
	w := httptest.NewRecorder()
	if path == "/api/auth/init" {
		s.authInit(w, r)
	} else {
		s.authLogin(w, r)
	}
	return w
}

func TestAuthHandlersThrottleRemoteIPBeforeDatabase(t *testing.T) {
	for _, path := range []string{"/api/auth/login", "/api/auth/init"} {
		t.Run(path, func(t *testing.T) {
			s := newAuthControlsServer(t)
			for attempt := 0; attempt < 6; attempt++ {
				// Changing the source port and untrusted proxy headers must not
				// grant more attempts to the same network peer.
				w := authControlsRequest(s, path, fmt.Sprintf("192.0.2.1:%d", 1000+attempt), fmt.Sprintf("198.51.100.%d", attempt+1))
				want := http.StatusServiceUnavailable
				if attempt == 5 {
					want = http.StatusTooManyRequests
				}
				if w.Code != want {
					t.Fatalf("attempt %d: status=%d want %d; body=%s", attempt+1, w.Code, want, w.Body.String())
				}
				if attempt == 5 && w.Header().Get("Retry-After") == "" {
					t.Fatal("throttled response is missing Retry-After")
				}
			}
		})
	}
}

func TestAuthThrottleCanonicalPeerAddresses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		peers []string
	}{
		{"IPv6", []string{
			"[2001:db8::1]:1000", "[2001:0db8:0:0:0:0:0:1]:1001",
			"[2001:db8::1]:1002", "2001:db8::1", "[2001:db8::1]:1004", "[2001:db8::1]:1005",
		}},
		{"IPv4-mapped IPv6", []string{
			"192.0.2.1:1000", "[::ffff:192.0.2.1]:1001", "[::ffff:c000:201]:1002",
			"192.0.2.1", "192.0.2.1:1004", "[::ffff:192.0.2.1]:1005",
		}},
		{"IPv6 zone", []string{
			"[fe80::1%en0]:1000", "[fe80::1%en1]:1001", "[fe80::1%en2]:1002",
			"[fe80::1]:1003", "fe80::1%en3", "[fe80::1%en4]:1005",
		}},
		{"malformed peers share a quota", []string{"", "invalid:1001", "invalid:1002", "invalid:1003", "invalid:1004", "invalid:1005"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAuthControlsServer(t)
			for i, peer := range tc.peers {
				w := authControlsRequest(s, "/api/auth/login", peer, "")
				want := http.StatusServiceUnavailable
				if i == 5 {
					want = http.StatusTooManyRequests
				}
				if w.Code != want {
					t.Fatalf("peer %q: status=%d want %d", peer, w.Code, want)
				}
			}
			if w := authControlsRequest(s, "/api/auth/login", "203.0.113.2:1000", ""); w.Code != http.StatusServiceUnavailable {
				t.Fatalf("a different peer did not get its own quota: status=%d", w.Code)
			}
		})
	}
}

func TestAuthThrottleConcurrentSharedBurst(t *testing.T) {
	s := newAuthControlsServer(t)
	now := time.Now()
	s.authLimiter.now = func() time.Time { return now }
	const requests = 64
	start := make(chan struct{})
	statuses := make(chan int, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			path := "/api/auth/login"
			if i%2 == 0 {
				path = "/api/auth/init"
			}
			statuses <- authControlsRequest(s, path, fmt.Sprintf("192.0.2.1:%d", 1000+i), fmt.Sprintf("198.51.100.%d", i)).Code
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	counts := make(map[int]int)
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusServiceUnavailable] != 5 || counts[http.StatusTooManyRequests] != requests-5 {
		t.Fatalf("concurrent requests reached handlers outside the shared five-attempt quota: %#v", counts)
	}
}

func TestAuthThrottleRetryAtWindowBoundary(t *testing.T) {
	s := newAuthControlsServer(t)
	start := time.Now()
	var now atomic.Int64
	now.Store(start.UnixNano())
	s.authLimiter.now = func() time.Time { return time.Unix(0, now.Load()) }
	for i := 0; i < 5; i++ {
		if w := authControlsRequest(s, "/api/auth/login", "192.0.2.1:1000", ""); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("attempt %d: status=%d want 503", i+1, w.Code)
		}
	}
	now.Store(start.Add(500 * time.Millisecond).UnixNano())
	w := authControlsRequest(s, "/api/auth/init", "192.0.2.1:2000", "")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" {
		t.Fatalf("remaining fractional second should round up: status=%d Retry-After=%q", w.Code, w.Header().Get("Retry-After"))
	}
	now.Store(start.Add(time.Minute - time.Nanosecond).UnixNano())
	w = authControlsRequest(s, "/api/auth/init", "192.0.2.1:2001", "")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("quota reset before expiry: status=%d Retry-After=%q", w.Code, w.Header().Get("Retry-After"))
	}
	now.Store(start.Add(time.Minute).UnixNano())
	w = authControlsRequest(s, "/api/auth/init", "192.0.2.1:2002", "")
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "" {
		t.Fatalf("window expiry should restore the handler: status=%d Retry-After=%q", w.Code, w.Header().Get("Retry-After"))
	}
}

func TestAuthThrottleBoundsStorageWithoutEvictingActivePeers(t *testing.T) {
	s := newAuthControlsServer(t)
	start := time.Now()
	var now atomic.Int64
	now.Store(start.UnixNano())
	s.authLimiter.now = func() time.Time { return time.Unix(0, now.Load()) }
	exhaustedPeer := "198.18.0.0:1000"
	for i := 0; i < 5; i++ {
		if w := authControlsRequest(s, "/api/auth/login", exhaustedPeer, ""); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("initial burst: status=%d want 503", w.Code)
		}
	}
	for i := 1; i < maxAuthClients; i++ {
		peer := fmt.Sprintf("198.18.%d.%d:1000", i/256, i%256)
		if w := authControlsRequest(s, "/api/auth/login", peer, ""); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("fill slot %d: status=%d want 503", i, w.Code)
		}
	}
	now.Store(start.Add(30 * time.Second).UnixNano())
	for i := 0; i < 5; i++ {
		w := authControlsRequest(s, "/api/auth/login", fmt.Sprintf("203.0.113.%d:1000", i), "")
		if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "30" {
			t.Fatalf("full table should reject new peers until space is available: status=%d Retry-After=%q", w.Code, w.Header().Get("Retry-After"))
		}
	}
	if got := len(s.authLimiter.clients); got != maxAuthClients {
		t.Fatalf("client storage grew beyond its bound: %d", got)
	}
	if w := authControlsRequest(s, "/api/auth/init", exhaustedPeer, ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("IP churn evicted an active exhausted quota: status=%d", w.Code)
	}
	if w := authControlsRequest(s, "/api/auth/init", "198.18.0.1:2000", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("full table should preserve existing peers' unused quota: status=%d", w.Code)
	}
	now.Store(start.Add(time.Minute).UnixNano())
	if w := authControlsRequest(s, "/api/auth/login", "203.0.113.1:2000", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expired buckets were not pruned: status=%d", w.Code)
	}
	if got := len(s.authLimiter.clients); got != 1 {
		t.Fatalf("expired buckets remained after pruning: %d", got)
	}
}

func TestAuthThrottlePreservesEarlierHandlerResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"malformed login", "{", http.StatusBadRequest},
		{"incorrect username", `{"username":"other","password":"test-password"}`, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAuthControlsServer(t)
			for i := 0; i < 6; i++ {
				r := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(tc.body))
				r.RemoteAddr = "192.0.2.1:1000"
				w := httptest.NewRecorder()
				s.authLogin(w, r)
				want := tc.want
				if i == 5 {
					want = http.StatusTooManyRequests
				}
				if w.Code != want {
					t.Fatalf("attempt %d: status=%d want %d", i+1, w.Code, want)
				}
			}
			w := httptest.NewRecorder()
			s.authStatus(w, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
			if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "" {
				t.Fatalf("auth status unexpectedly consumed the login/init quota: status=%d Retry-After=%q", w.Code, w.Header().Get("Retry-After"))
			}
		})
	}
}
