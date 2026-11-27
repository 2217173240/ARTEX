package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

type fakePoolMCPClient struct {
	id     int64
	rev    int64
	closed atomic.Bool
}

func (f *fakePoolMCPClient) Tools(context.Context) ([]actool.CoreTool, error) {
	return nil, nil
}

func (f *fakePoolMCPClient) Close() error {
	f.closed.Store(true)
	return nil
}

func fakePoolTool(name string) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        name,
		Description: "fake tool",
		Schema:      map[string]any{"type": "object"},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			return actool.Text("ok"), nil
		},
	})
}

func TestMCPClientPoolReusesClientAcrossAcquires(t *testing.T) {
	t.Parallel()
	pool := newMCPClientPool()
	var dials atomic.Int32
	var authChecks atomic.Int32
	var clients []*fakePoolMCPClient
	var mu sync.Mutex

	pool.setupFn = func(ctx context.Context, pg *db.DB, id, revision int64, budget time.Duration) (mcpClient, []actool.CoreTool, error) {
		dials.Add(1)
		cl := &fakePoolMCPClient{id: id, rev: revision}
		mu.Lock()
		clients = append(clients, cl)
		mu.Unlock()
		return cl, []actool.CoreTool{fakePoolTool("mcp__srv__ping")}, nil
	}
	pool.authFn = func(ctx context.Context, pg *db.DB, id, revision int64) error {
		authChecks.Add(1)
		return nil
	}

	pg := &db.DB{}
	ts1, err := pool.acquire(context.Background(), pg, 42, 1, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ts2, err := pool.acquire(context.Background(), pg, 42, 1, time.Second)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if len(ts1) != 1 || len(ts2) != 1 || ts1[0].Name() != "mcp__srv__ping" || ts2[0].Name() != "mcp__srv__ping" {
		t.Fatalf("unexpected tools: ts1=%v ts2=%v", ts1, ts2)
	}
	if dials.Load() != 1 {
		t.Fatalf("dials = %d, want 1", dials.Load())
	}
	if authChecks.Load() != 1 {
		t.Fatalf("pool hit authChecks = %d, want 1", authChecks.Load())
	}
	if clients[0].closed.Load() {
		t.Fatal("reused client was closed unexpectedly")
	}
	pool.closeForDB(pg)
	if !clients[0].closed.Load() {
		t.Fatal("closeForDB did not close pooled client")
	}
}

func TestMCPClientPoolDeduplicatesConcurrentAcquires(t *testing.T) {
	t.Parallel()
	pool := newMCPClientPool()
	var dials atomic.Int32
	dialEntered := make(chan struct{})
	releaseDial := make(chan struct{})

	pool.setupFn = func(ctx context.Context, pg *db.DB, id, revision int64, budget time.Duration) (mcpClient, []actool.CoreTool, error) {
		if dials.Add(1) == 1 {
			close(dialEntered)
		}
		<-releaseDial
		return &fakePoolMCPClient{id: id, rev: revision}, []actool.CoreTool{fakePoolTool("mcp__srv__tool")}, nil
	}
	pool.authFn = func(context.Context, *db.DB, int64, int64) error { return nil }

	pg := &db.DB{}
	const workers = 6
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			ts, err := pool.acquire(context.Background(), pg, 7, 3, time.Second)
			if err == nil && len(ts) != 1 {
				err = fmt.Errorf("expected 1 tool, got %d", len(ts))
			}
			errs <- err
		}()
	}

	<-dialEntered
	time.Sleep(20 * time.Millisecond)
	close(releaseDial)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent acquire failed: %v", err)
		}
	}
	if dials.Load() != 1 {
		t.Fatalf("concurrent acquires dialed %d times, want 1", dials.Load())
	}
	pool.closeForDB(pg)
}

func TestMCPClientPoolDoesNotCacheFailureAndRecovers(t *testing.T) {
	t.Parallel()
	pool := newMCPClientPool()
	var dials atomic.Int32
	var fail atomic.Bool
	fail.Store(true)

	pool.setupFn = func(ctx context.Context, pg *db.DB, id, revision int64, budget time.Duration) (mcpClient, []actool.CoreTool, error) {
		dials.Add(1)
		if fail.Load() {
			return nil, nil, errors.New("dial failed")
		}
		return &fakePoolMCPClient{id: id, rev: revision}, []actool.CoreTool{fakePoolTool("mcp__srv__ok")}, nil
	}
	pool.authFn = func(context.Context, *db.DB, int64, int64) error { return nil }

	pg := &db.DB{}
	if _, err := pool.acquire(context.Background(), pg, 9, 1, time.Second); err == nil {
		t.Fatal("expected initial acquire to fail")
	}
	fail.Store(false)
	ts, err := pool.acquire(context.Background(), pg, 9, 1, time.Second)
	if err != nil || len(ts) != 1 {
		t.Fatalf("recovery acquire failed: ts=%v err=%v", ts, err)
	}
	if dials.Load() != 2 {
		t.Fatalf("dials = %d, want 2 (failure must not be cached)", dials.Load())
	}
	pool.closeForDB(pg)
}

func TestMCPClientPoolEvictAndRevisionBumpFailClosed(t *testing.T) {
	t.Parallel()
	pool := newMCPClientPool()
	var activeRev atomic.Int64
	activeRev.Store(1)

	var mu sync.Mutex
	byRev := map[int64]*fakePoolMCPClient{}
	pool.setupFn = func(ctx context.Context, pg *db.DB, id, revision int64, budget time.Duration) (mcpClient, []actool.CoreTool, error) {
		if revision != activeRev.Load() {
			return nil, nil, db.ErrMCPUnauthorized
		}
		cl := &fakePoolMCPClient{id: id, rev: revision}
		mu.Lock()
		byRev[revision] = cl
		mu.Unlock()
		return cl, []actool.CoreTool{fakePoolTool(fmt.Sprintf("mcp__srv__v%d", revision))}, nil
	}
	pool.authFn = func(ctx context.Context, pg *db.DB, id, revision int64) error {
		if revision != activeRev.Load() {
			return db.ErrMCPUnauthorized
		}
		return nil
	}

	pg := &db.DB{}
	if _, err := pool.acquire(context.Background(), pg, 10, 1, time.Second); err != nil {
		t.Fatalf("acquire rev 1: %v", err)
	}

	// Revision bumps to 2: acquiring rev 2 must close rev 1, and stale rev 1 must fail closed.
	activeRev.Store(2)
	if _, err := pool.acquire(context.Background(), pg, 10, 1, time.Second); !errors.Is(err, db.ErrMCPUnauthorized) {
		t.Fatalf("stale rev 1 on hit = %v, want ErrMCPUnauthorized", err)
	}
	if !byRev[1].closed.Load() {
		t.Fatal("stale rev 1 client was not closed on unauthorized pool hit")
	}

	// Re-acquire rev 1 fresh, then bump to rev 2 and acquire rev 2 directly.
	activeRev.Store(1)
	if _, err := pool.acquire(context.Background(), pg, 10, 1, time.Second); err != nil {
		t.Fatalf("re-acquire rev 1: %v", err)
	}
	rev1Second := byRev[1]
	activeRev.Store(2)
	ts2, err := pool.acquire(context.Background(), pg, 10, 2, time.Second)
	if err != nil || len(ts2) != 1 || ts2[0].Name() != "mcp__srv__v2" {
		t.Fatalf("acquire rev 2: ts=%v err=%v", ts2, err)
	}
	if !rev1Second.closed.Load() {
		t.Fatal("revision bump did not close old rev 1 client")
	}
	if byRev[2].closed.Load() {
		t.Fatal("active rev 2 client was closed prematurely")
	}

	// Explicit evict closes rev 2 and keeps isolated DBs untouched.
	otherPG := &db.DB{}
	activeRev.Store(5)
	if _, err := pool.acquire(context.Background(), otherPG, 10, 5, time.Second); err != nil {
		t.Fatalf("otherPG acquire: %v", err)
	}
	otherClient := byRev[5]
	pool.evict(pg, 10)
	if !byRev[2].closed.Load() {
		t.Fatal("evict did not close rev 2 client")
	}
	if otherClient.closed.Load() {
		t.Fatal("evict closed client belonging to another DB instance")
	}
	pool.closeForDB(otherPG)
	if !otherClient.closed.Load() {
		t.Fatal("closeForDB did not close otherPG client")
	}
}

func TestMCPClientPoolCallerCancelPreservesHealthyClient(t *testing.T) {
	t.Parallel()
	pool := newMCPClientPool()
	cl := &fakePoolMCPClient{id: 11, rev: 1}
	pool.setupFn = func(ctx context.Context, pg *db.DB, id, revision int64, budget time.Duration) (mcpClient, []actool.CoreTool, error) {
		return cl, []actool.CoreTool{fakePoolTool("mcp__srv__ok")}, nil
	}
	pool.authFn = func(ctx context.Context, pg *db.DB, id, revision int64) error {
		return ctx.Err()
	}

	pg := &db.DB{}
	if _, err := pool.acquire(context.Background(), pg, 11, 1, time.Second); err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pool.acquire(canceledCtx, pg, 11, 1, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquire err = %v, want context.Canceled", err)
	}
	if cl.closed.Load() {
		t.Fatal("caller cancellation closed shared healthy pooled client")
	}
	ts, err := pool.acquire(context.Background(), pg, 11, 1, time.Second)
	if err != nil || len(ts) != 1 {
		t.Fatalf("subsequent healthy acquire failed: ts=%v err=%v", ts, err)
	}
	pool.closeForDB(pg)
}
