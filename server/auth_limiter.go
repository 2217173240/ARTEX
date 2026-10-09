package server

import (
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

// authRequestLimiter uses fixed windows starting at each peer's first attempt.
// Login and initialization share it, so switching endpoints does not reset the
// quota. Only RemoteAddr identifies a peer; forwarding headers are untrusted.
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
	ip := authRemoteIP(remoteAddr)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	if l.clients == nil {
		l.clients = make(map[string]authAttemptBucket)
	}
	if bucket, ok := l.clients[ip]; ok && now.Before(bucket.expires) {
		if bucket.attempts >= authAttemptLimit {
			return bucket.expires.Sub(now)
		}
		bucket.attempts++
		l.clients[ip] = bucket
		return 0
	}
	if _, exists := l.clients[ip]; !exists && len(l.clients) >= maxAuthClients {
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
	l.clients[ip] = authAttemptBucket{expires: now.Add(authAttemptPeriod), attempts: 1}
	return 0
}

func (s *Server) allowAuthAttempt(w http.ResponseWriter, r *http.Request) bool {
	if retry := s.authLimiter.reserve(r.RemoteAddr); retry > 0 {
		seconds := (retry + time.Second - 1) / time.Second
		w.Header().Set("Retry-After", strconv.FormatInt(int64(seconds), 10))
		writeErr(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后重试")
		return false
	}
	return true
}
