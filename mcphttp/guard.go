package mcphttp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrCircuitOpen = errors.New("mcp: circuit open")
var ErrClosed = errors.New("mcp: client closed")

// Options applies to one Client only. Zero values use defaults; negative values
// are rejected. Requests are never retried, including the recovery probe.
type Options struct {
	MaxConcurrent    int
	FailureThreshold int
	Cooldown         time.Duration
}

func (o Options) defaults() (Options, error) {
	if o.MaxConcurrent < 0 || o.FailureThreshold < 0 || o.Cooldown < 0 {
		return o, fmt.Errorf("mcp: options must not be negative")
	}
	if o.MaxConcurrent == 0 {
		o.MaxConcurrent = 2
	}
	if o.FailureThreshold == 0 {
		o.FailureThreshold = 5
	}
	if o.Cooldown == 0 {
		o.Cooldown = time.Minute
	}
	return o, nil
}

type circuit struct {
	mu                 sync.Mutex
	options            Options
	changed            chan struct{}
	closed             bool
	inflight, failures int
	epoch              uint64
	state              string
	until              time.Time
	now                func() time.Time
	ctx                context.Context
	cancel             context.CancelFunc
}
type admission struct {
	epoch uint64
	probe bool
}

func newCircuit(o Options) *circuit {
	ctx, cancel := context.WithCancel(context.Background())
	return &circuit{options: o, changed: make(chan struct{}), state: "closed", now: time.Now, ctx: ctx, cancel: cancel}
}
func (g *circuit) wake() { close(g.changed); g.changed = make(chan struct{}) }
func (g *circuit) acquire(ctx context.Context) (admission, error) {
	for {
		g.mu.Lock()
		if err := ctx.Err(); err != nil {
			g.mu.Unlock()
			return admission{}, err
		}
		if g.closed {
			g.mu.Unlock()
			return admission{}, ErrClosed
		}
		if g.state == "half-open" || (g.state == "open" && g.now().Before(g.until)) {
			g.mu.Unlock()
			return admission{}, ErrCircuitOpen
		}
		if g.inflight < g.options.MaxConcurrent {
			a := admission{epoch: g.epoch}
			if g.state == "open" {
				g.state = "half-open"
				a.probe = true
			}
			g.inflight++
			g.mu.Unlock()
			return a, nil
		}
		ch := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return admission{}, ctx.Err()
		case <-ch:
		}
	}
}
func (g *circuit) finish(a admission, err error, canceled bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inflight--
	defer g.wake()
	if g.closed || a.epoch != g.epoch {
		return
	}
	if canceled {
		if a.probe {
			g.state = "open"
		} // allow another probe without declaring unhealthy
		return
	}
	if err == nil {
		g.failures = 0
		if a.probe {
			g.state = "closed"
			g.epoch++
		}
		return
	}
	g.failures++
	if a.probe || g.failures >= g.options.FailureThreshold {
		g.state = "open"
		g.until = g.now().Add(g.options.Cooldown)
		g.epoch++
	}
}
func (g *circuit) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.closed {
		g.closed = true
		g.cancel()
		g.wake()
	}
}
func (c *Client) circuit() *circuit {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.guard == nil {
		o, _ := (Options{}).defaults()
		c.guard = newCircuit(o)
	}
	return c.guard
}
