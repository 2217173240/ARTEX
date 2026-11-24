package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

const (
	// ARTEX has one administrator: five attempts per peer per minute allows
	// ordinary typing retries while bounding unauthenticated DB/bcrypt work.
	authAttemptLimit  = 5
	authAttemptPeriod = time.Minute
	// Bound memory even when many different peers send authentication requests.
	// A full table rejects new peers until a bucket expires, preserving active
	// quotas rather than letting IP churn reset them through eviction.
	maxAuthClients = 1024
)

type authAttemptBucket struct {
	expires  time.Time
	attempts int
}

// authRequestLimiter uses bounded, expiring fixed windows per key. The peer
// quota is shared by login, initialization and authenticated password changes.
// Password changes also use a separate instance keyed by session digest.
// Only RemoteAddr identifies a peer; forwarding headers are untrusted.
// Behind a reverse proxy, the gateway must enforce limits per actual client,
// since ARTEX sees the gateway's IP and applies one shared quota to that peer.
// The zero value is usable and expired entries are pruned during requests; no
// background goroutine or externally configurable allocation is needed.
type authRequestLimiter struct {
	mu      sync.Mutex
	clients map[string]authAttemptBucket
	now     func() time.Time // optional clock for deterministic handler tests
}

func authRemoteIP(remoteAddr string) string {
	if peer, err := netip.ParseAddrPort(remoteAddr); err == nil {
		return peer.Addr().Unmap().WithZone("").String()
	}
	if peer, err := netip.ParseAddr(remoteAddr); err == nil {
		return peer.Unmap().WithZone("").String()
	}
	// Malformed/missing addresses share one bucket instead of allocating a
	// separate quota for arbitrary strings. net/http normally supplies IP:port.
	return ""
}

// reserve consumes one attempt, or returns the time until an attempt is allowed.
func (l *authRequestLimiter) reserve(remoteAddr string) time.Duration {
	return l.reserveKey(authRemoteIP(remoteAddr))
}

func (l *authRequestLimiter) reserveKey(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	if l.clients == nil {
		l.clients = make(map[string]authAttemptBucket)
	}
	if bucket, ok := l.clients[key]; ok && now.Before(bucket.expires) {
		if bucket.attempts >= authAttemptLimit {
			return bucket.expires.Sub(now)
		}
		bucket.attempts++
		l.clients[key] = bucket
		return 0
	}
	if _, exists := l.clients[key]; !exists && len(l.clients) >= maxAuthClients {
		// Scan only when capacity is needed, keeping the normal peer path O(1).
		// Expired buckets can be removed safely; live ones retain their quota.
		retry := authAttemptPeriod
		for client, bucket := range l.clients {
			if !now.Before(bucket.expires) {
				delete(l.clients, client)
			} else if remaining := bucket.expires.Sub(now); remaining < retry {
				retry = remaining
			}
		}
		if len(l.clients) >= maxAuthClients {
			return retry
		}
	}
	l.clients[key] = authAttemptBucket{expires: now.Add(authAttemptPeriod), attempts: 1}
	return 0
}

func (s *Server) allowAuthAttempt(w http.ResponseWriter, r *http.Request) bool {
	return allowAuthRetry(w, s.authLimiter.reserve(r.RemoteAddr))
}

// Only verified administrator sessions reach this quota. Store a fixed-size
// digest rather than credentials, and reject exhausted sessions before they
// can consume a new peer's login quota.
func (s *Server) allowPasswordChangeAttempt(w http.ResponseWriter, r *http.Request) bool {
	digest := sha256.Sum256([]byte(extractToken(r)))
	if !allowAuthRetry(w, s.passwordChangeLimiter.reserveKey(hex.EncodeToString(digest[:]))) {
		return false
	}
	return s.allowAuthAttempt(w, r)
}

func allowAuthRetry(w http.ResponseWriter, retry time.Duration) bool {
	if retry > 0 {
		seconds := (retry + time.Second - 1) / time.Second
		w.Header().Set("Retry-After", strconv.FormatInt(int64(seconds), 10))
		writeErr(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后重试")
		return false
	}
	return true
}
