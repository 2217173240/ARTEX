package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkerSettingsResizeActivePool(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("explicit isolated ARTEX_PG_DSN required")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	p := newWorkerPool(ctx, 1, func(ctx context.Context, _ int, _ *workerPool) { <-ctx.Done() })
	defer func() { cancel(); p.Close() }()
	e := &Engine{m: m, runtimes: map[string]*taskRuntime{"active": {ctx: ctx, pool: p}}}
	s := &Server{m: m, engine: e}
	for _, want := range []int{4, 2} {
		w := httptest.NewRecorder()
		s.putSettings(w, httptest.NewRequest("PUT", "/api/settings", strings.NewReader(fmt.Sprintf(`{"workers":%d}`, want))))
		if w.Code != 200 || m.Workers() != want {
			t.Fatalf("settings save: status=%d workers=%d", w.Code, m.Workers())
		}
		p.mu.Lock()
		got := p.limit
		p.mu.Unlock()
		if got != want || ctx.Err() != nil {
			t.Fatalf("active pool limit=%d, want=%d, ctx=%v", got, want, ctx.Err())
		}
	}
	w := httptest.NewRecorder()
	s.putSettings(w, httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"workers":0}`)))
	if w.Code != 400 || m.Workers() != 2 {
		t.Fatal("invalid limit changed persisted worker count")
	}
}

func TestWorkerPoolFailedReadPreservesCapacity(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("explicit isolated ARTEX_PG_DSN required")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.SetWorkers(4); err != nil {
		t.Fatal(err)
	}
	p := newWorkerPool(context.Background(), 4, func(ctx context.Context, _ int, _ *workerPool) { <-ctx.Done() })
	defer p.Close()
	e := &Engine{m: m, runtimes: map[string]*taskRuntime{"synthetic": {pool: p}}}
	m.pg.Close()
	if err := e.ReconcileWorkers(); err == nil {
		t.Fatal("storage read failure was swallowed")
	}
	p.mu.Lock()
	got := p.limit
	p.mu.Unlock()
	if got != 4 {
		t.Fatalf("failed read changed pool capacity to %d", got)
	}
	if m.Workers() != defaultWorkers {
		t.Fatal("public getter lost fallback contract")
	}
	s := &Server{m: m, engine: e}
	w := httptest.NewRecorder()
	s.putSettings(w, httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"workers":6}`)))
	if w.Code != 400 {
		t.Fatalf("failed persist status=%d", w.Code)
	}
	p.mu.Lock()
	got = p.limit
	p.mu.Unlock()
	if got != 4 {
		t.Fatalf("failed persist reconciled pool to %d", got)
	}
}

func TestWorkerPoolLiveResizeDrainsWithoutInterrupting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan int, 32)
	finish := make(chan struct{})
	p := newWorkerPool(ctx, 1, func(ctx context.Context, i int, p *workerPool) {
		for p.allowed(i) {
			if !p.begin(i) {
				if sleepCtx(ctx, time.Millisecond) {
					return
				}
				continue
			}
			started <- i
			select {
			case <-finish:
			case <-ctx.Done():
			}
			p.end()
		}
	})
	defer func() { cancel(); p.Close() }()
	next := func() {
		t.Helper()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("worker did not start")
		}
	}
	next()
	p.Resize(3)
	next()
	next()
	p.Resize(1)
	// Active work remains alive. No new step may start even if a low-index worker
	// finishes before the high-index workers that are draining.
	finish <- struct{}{}
	select {
	case <-started:
		t.Fatal("new work started above lowered limit")
	case <-time.After(20 * time.Millisecond):
	}
	finish <- struct{}{}
	select {
	case <-started:
		t.Fatal("new work started while capacity still full")
	case <-time.After(20 * time.Millisecond):
	}
	finish <- struct{}{}
	next()
}

func TestWorkerPoolResizeRacesWithShutdown(t *testing.T) {
	for range 20 {
		ctx, cancel := context.WithCancel(context.Background())
		p := newWorkerPool(ctx, 3, func(ctx context.Context, i int, p *workerPool) {
			for p.allowed(i) {
				if p.begin(i) {
					p.end()
				}
				if sleepCtx(ctx, time.Millisecond) {
					return
				}
			}
		})
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					p.Resize(i%5 + 1)
				}
			}()
		}
		cancel()
		p.Close()
		wg.Wait()
		p.mu.Lock()
		left, busy := len(p.workers), p.busy
		p.mu.Unlock()
		if left != 0 || busy != 0 {
			t.Fatalf("pool leaked workers=%d busy=%d", left, busy)
		}
	}
}

// A retiring index must remain owned until its old loop has returned, even when
// a settings change revives it before it notices the lower limit.
func TestWorkerPoolShrinkGrowKeepsSingleSlotOwner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan int, 16)
	p := newWorkerPool(ctx, 4, func(ctx context.Context, i int, _ *workerPool) {
		started <- i
		<-ctx.Done()
	})
	defer p.Close()
	for range 4 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("initial loop missing")
		}
	}
	for range 100 {
		p.Resize(1)
		p.Resize(4)
	}
	select {
	case i := <-started:
		t.Fatalf("duplicate slot %d", i)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestWorkerPoolCloseCancelsAndJoinsWithoutParentCancel(t *testing.T) {
	finished := make(chan struct{})
	p := newWorkerPool(context.Background(), 1, func(ctx context.Context, _ int, _ *workerPool) {
		<-ctx.Done()
		close(finished)
	})
	p.Close()
	p.Close()
	p.Resize(6)
	select {
	case <-finished:
	default:
		t.Fatal("Close returned before loop joined")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.workers) != 0 {
		t.Fatal("closed pool spawned workers")
	}
}

func TestWorkerSettingsResizeRealIdleTaskLifecycle(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("explicit isolated ARTEX_PG_DSN required")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.SetWorkers(1); err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("synthetic idle pool", "no model or target calls", nil, 0, 600)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Run(ctx, task)
	defer e.StopTask(task.ID)
	s := &Server{m: m, engine: e}
	for _, want := range []int{4, 1, 6} {
		w := httptest.NewRecorder()
		s.putSettings(w, httptest.NewRequest("PUT", "/api/settings", strings.NewReader(fmt.Sprintf(`{"workers":%d}`, want))))
		if w.Code != 200 {
			t.Fatalf("settings: %d %s", w.Code, w.Body.String())
		}
		e.runtimeMu.Lock()
		p := e.runtimes[task.ID].pool
		p.mu.Lock()
		got := p.limit
		p.mu.Unlock()
		e.runtimeMu.Unlock()
		if got != want {
			t.Fatalf("running task limit=%d want=%d", got, want)
		}
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				e.ReconcileWorkers()
				e.Run(ctx, task)
			}
		}()
	}
	e.BeginDelete(task.ID)
	wg.Wait()
	// Drain Run callers while the delete barrier is installed. StopTask clears
	// the barrier after joining; an explicit later Run is a valid new admission.
	e.StopTask(task.ID)
	e.runtimeMu.Lock()
	remaining := len(e.runtimes)
	e.runtimeMu.Unlock()
	if remaining != 0 {
		t.Fatalf("runtime remained after StopTask: %d", remaining)
	}
}
