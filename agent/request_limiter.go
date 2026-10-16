package agent

// Adapted from RuoJi6 (ji ruo), ARTEX PR #13, commit
// bfc5e6236e7197868f2ed8d0598101eb3de7dc0b (AGPL-3.0).
// ARTEX keeps its existing transports/timeouts and bounds retained history.

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"
)

// RequestLimiter enforces both rolling HTTP attempt windows, (now-period, now].
// Zero disables a window; positive rates >= 1 allow floor(rate) attempts per
// second/minute. Rates below 1 allow one attempt per ceil(period/rate).
// History survives reconfiguration, including attempts while unlimited. History
// buckets coalesce attempts less than 10ms apart and expire at the latest attempt
// in the bucket: this is conservative, adding <10ms delay without over-admission.
// At most about 6,002 buckets cover a minute. Only the latest older attempt is
// needed for fractional windows (whose capacity is one), avoiding unbounded
// history for tiny rates. The zero value is ready for use. Scope is one process.
type RequestLimiter struct {
	mu      sync.Mutex
	windows [2]requestWindow
	sent    []requestBucket
	changed chan struct{}
}

type requestWindow struct {
	period time.Duration
	limit  float64
}
type requestBucket struct {
	first, last time.Time
	count       uint64
}

const requestHistoryResolution = 10 * time.Millisecond

// ValidateRequestRates checks rate settings without constructing a provider or
// modifying quota history. Persistence boundaries must call it before writing.
func ValidateRequestRates(perSecond, perMinute float64) error {
	_, err := requestWindows(perSecond, perMinute)
	return err
}

func requestWindows(perSecond, perMinute float64) ([2]requestWindow, error) {
	var windows [2]requestWindow
	for i, rate := range [2]float64{perSecond, perMinute} {
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
			return windows, fmt.Errorf("llm: request rate must be finite and non-negative")
		}
		// Above this boundary float64 cannot represent every whole request count.
		if rate > 1<<53-1 {
			return windows, fmt.Errorf("llm: request rate is too large")
		}
		if rate == 0 {
			continue
		}
		period, limit := [2]time.Duration{time.Second, time.Minute}[i], math.Floor(rate)
		if rate < 1 {
			nanos := math.Ceil(float64(period) / rate)
			if nanos >= float64(math.MaxInt64) {
				return windows, fmt.Errorf("llm: request rate is too small")
			}
			period, limit = time.Duration(nanos), 1
		}
		windows[i] = requestWindow{period, limit}
	}
	return windows, nil
}

func (l *RequestLimiter) configure(perSecond, perMinute float64) error {
	windows, err := requestWindows(perSecond, perMinute)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.windows = windows
	if l.changed != nil {
		close(l.changed)
	}
	l.changed = make(chan struct{})
	return nil
}

// wait admits at the time capacity is available, without future reservations.
// No callbacks or network I/O run while holding the history lock.
func (l *RequestLimiter) wait(ctx context.Context) error {
	if l == nil {
		return ctx.Err()
	}
	for {
		l.mu.Lock()
		if err := ctx.Err(); err != nil {
			l.mu.Unlock()
			return err
		}
		now := time.Now()
		// Keep a minute for future second/minute rate changes and the most recent
		// older bucket for any future fractional window. Compact the backing array
		// in place so expired entries do not retain ever-growing allocations.
		first := 0
		for first+1 < len(l.sent) && !l.sent[first].last.After(now.Add(-time.Minute)) {
			first++
		}
		if first > 0 {
			l.sent = l.sent[:copy(l.sent, l.sent[first:])]
		}
		var delay time.Duration
		for _, w := range l.windows {
			if w.limit == 0 {
				continue
			}
			var count float64
			for i := len(l.sent) - 1; i >= 0; i-- {
				b := l.sent[i]
				if !b.last.After(now.Add(-w.period)) {
					break
				}
				count += float64(b.count)
				if count >= w.limit {
					if d := b.last.Add(w.period).Sub(now); d > delay {
						delay = d
					}
					break
				}
			}
		}
		if delay <= 0 {
			n := len(l.sent)
			if n > 0 && now.Sub(l.sent[n-1].first) < requestHistoryResolution && l.sent[n-1].count < math.MaxUint64 {
				l.sent[n-1].last = now
				l.sent[n-1].count++
			} else {
				l.sent = append(l.sent, requestBucket{now, now, 1})
			}
			l.mu.Unlock()
			return nil
		}
		changed := l.changed
		l.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}

type rateLimitedTransport struct {
	base    http.RoundTripper
	limiter *RequestLimiter
}

func (t rateLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.limiter.wait(req.Context()); err != nil {
		// RoundTripper owns the body even when admission fails. The underlying
		// transport never sees this request, so close it here without reading it.
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}
