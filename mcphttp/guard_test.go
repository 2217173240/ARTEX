package mcphttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testCircuit() *circuit {
	return newCircuit(Options{MaxConcurrent: 2, FailureThreshold: 1, Cooldown: time.Minute})
}
func TestCircuitEpochProbeAndCancellation(t *testing.T) {
	g := testCircuit()
	defer g.close()
	now := time.Unix(1, 0)
	g.now = func() time.Time { return now }
	old, _ := g.acquire(context.Background())
	bad, _ := g.acquire(context.Background())
	g.finish(bad, errors.New("failure"), false)
	g.finish(old, nil, false)
	if _, err := g.acquire(context.Background()); !errors.Is(err, ErrCircuitOpen) {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	probe, err := g.acquire(context.Background())
	if err != nil || !probe.probe {
		t.Fatal(probe, err)
	}
	if _, err := g.acquire(context.Background()); !errors.Is(err, ErrCircuitOpen) {
		t.Fatal(err)
	}
	g.finish(probe, errors.New("probe failed"), false)
	if g.state != "open" {
		t.Fatal(g.state)
	}
	now = now.Add(time.Minute)
	probe, _ = g.acquire(context.Background())
	g.finish(probe, context.Canceled, true)
	probe, err = g.acquire(context.Background())
	if err != nil || !probe.probe {
		t.Fatal(probe, err)
	}
	g.finish(probe, nil, false)
	if g.state != "closed" {
		t.Fatal(g.state)
	}
}
func TestCircuitQueueCancellationAndClose(t *testing.T) {
	g := testCircuit()
	a, _ := g.acquire(context.Background())
	b, _ := g.acquire(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if g.inflight != 2 {
		t.Fatal(g.inflight)
	}
	done := make(chan error, 1)
	go func() { _, err := g.acquire(context.Background()); done <- err }()
	g.close()
	if err := <-done; !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	g.finish(a, nil, false)
	g.finish(b, nil, false)
	if g.inflight != 0 {
		t.Fatal(g.inflight)
	}
}
func TestHTTPGuardIsolationBusinessErrorsAndRedaction(t *testing.T) {
	var fail atomic.Bool
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		if fail.Load() {
			w.WriteHeader(500)
			w.Write([]byte("SECRET_BODY"))
			return
		}
		json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid arguments"}})
	}))
	defer srv.Close()
	makeClient := func() *Client { return &Client{server: "same", url: srv.URL, http: srv.Client(), guard: testCircuit()} }
	a, b := makeClient(), makeClient()
	defer a.Close()
	defer b.Close()
	if _, err := a.call(context.Background(), "tools/call", nil); err == nil {
		t.Fatal("business error missing")
	}
	if a.guard.state != "closed" {
		t.Fatal("business error opened circuit")
	}
	fail.Store(true)
	if _, err := a.call(context.Background(), "tools/call", nil); err == nil || err.Error() != "mcp: transport request failed" {
		t.Fatal(err)
	}
	before := requests.Load()
	if _, err := a.call(context.Background(), "tools/call", nil); !errors.Is(err, ErrCircuitOpen) {
		t.Fatal(err)
	}
	if requests.Load() != before {
		t.Fatal("open circuit sent request")
	}
	fail.Store(false)
	if _, err := b.call(context.Background(), "tools/call", nil); errors.Is(err, ErrCircuitOpen) {
		t.Fatal("clients share state")
	}
}
func TestHTTPCloseCancelsInflight(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer srv.Close()
	c := &Client{url: srv.URL, http: srv.Client()}
	done := make(chan error, 1)
	go func() { _, err := c.call(context.Background(), "test", nil); done <- err }()
	<-started
	c.Close()
	if err := <-done; !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	<-stopped
	if _, err := c.call(context.Background(), "test", nil); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
func TestOptionsValidation(t *testing.T) {
	for _, o := range []Options{{MaxConcurrent: -1}, {FailureThreshold: -1}, {Cooldown: -1}} {
		if _, err := NewWithOptions(context.Background(), "x", "http://unused", nil, false, o); err == nil {
			t.Fatal(o)
		}
		if _, err := NewSSEWithOptions(context.Background(), "x", "http://unused", nil, false, o); err == nil {
			t.Fatal(o)
		}
	}
}

func TestLegacySSEGuardRecoveryAndClose(t *testing.T) {
	messages := make(chan []byte, 8)
	calls := make(chan struct{}, 8)
	var failing atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("event: endpoint\ndata: /message\n\n"))
			w.(http.Flusher).Flush()
			for {
				select {
				case payload := <-messages:
					w.Write(append(append([]byte("data: "), payload...), []byte("\n\n")...))
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		}
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "tools/call" {
			calls <- struct{}{}
			if failing.Load() {
				w.WriteHeader(503)
				return
			}
		}
		w.WriteHeader(202)
		if req.ID != 0 {
			payload, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"content":[]}`)})
			messages <- payload
		}
	}))
	defer srv.Close()
	c, err := NewSSEWithOptions(context.Background(), "same", srv.URL+"/sse", nil, false, Options{FailureThreshold: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	now := time.Unix(1, 0)
	c.guard.now = func() time.Time { return now }
	failing.Store(true)
	if _, err := c.Call(context.Background(), "test", nil); err == nil {
		t.Fatal("missing failure")
	}
	<-calls
	if _, err := c.Call(context.Background(), "test", nil); !errors.Is(err, ErrCircuitOpen) {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	failing.Store(false)
	if _, err := c.Call(context.Background(), "test", nil); err != nil {
		t.Fatal(err)
	}
	<-calls
	if c.guard.state != "closed" {
		t.Fatal(c.guard.state)
	}
	c.Close()
	if _, err := c.Call(context.Background(), "test", nil); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestHTTPBoundedConcurrencyAndQueuedCancellation(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		entered <- struct{}{}
		<-release
		json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{}`)})
	}))
	defer srv.Close()
	c := &Client{url: srv.URL, http: srv.Client(), guard: testCircuit()}
	defer c.Close()
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := c.call(context.Background(), "test", nil); results <- err }()
	}
	<-entered
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan error, 1)
	go func() { _, err := c.call(ctx, "test", nil); queued <- err }()
	cancel()
	if err := <-queued; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-entered:
		t.Fatal("canceled queued request reached server")
	default:
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if c.guard.failures != 0 {
		t.Fatal("cancellation affected health")
	}
}
