package enrich

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEngineCloseWithoutEnqueueIsLazyAndRaceFree(t *testing.T) {
	e := New(nil, nil, 0)
	if e.workers != 4 {
		t.Fatalf("expected default workers=4, got %d", e.workers)
	}
	e.mu.Lock()
	startedBefore := e.started
	resolvBefore := e.resolv
	e.mu.Unlock()
	if startedBefore || resolvBefore != nil {
		t.Fatalf("expected lazy startup before enqueue, started=%v resolv=%v", startedBefore, resolvBefore)
	}

	e.Close()
	e.Close() // idempotent

	e.mu.Lock()
	startedAfter := e.started
	resolvAfter := e.resolv
	closed := e.isClosed
	e.mu.Unlock()
	if startedAfter || resolvAfter != nil || !closed {
		t.Fatalf("expected closed without starting workers, started=%v resolv=%v closed=%v", startedAfter, resolvAfter, closed)
	}
}

func TestEngineLazyStartProcessesJobAndStopsCleanly(t *testing.T) {
	hit := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case hit <- r.URL.Path:
		default:
		}
		_, _ = w.Write([]byte("<title>ok</title>"))
	}))
	defer srv.Close()

	e := New(nil, nil, 2)
	e.mu.Lock()
	startedBefore := e.started
	e.mu.Unlock()
	if startedBefore {
		t.Fatal("expected workers not started before enqueue")
	}

	e.ProbeSite(1, srv.URL+"/probe")

	select {
	case path := <-hit:
		if path != "/probe" {
			t.Fatalf("unexpected request path %q", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lazily started worker to process HTTP probe")
	}

	e.mu.Lock()
	startedAfter := e.started
	e.mu.Unlock()
	if !startedAfter {
		t.Fatal("expected workers to be marked started after enqueue")
	}

	e.Close()
}

func TestEngineEnqueueAfterCloseIsNoOp(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New(nil, nil, 2)
	e.Close()

	e.ProbeSite(1, srv.URL)
	e.ResolveDomain(2, "example.com")

	e.mu.Lock()
	started := e.started
	resolv := e.resolv
	e.mu.Unlock()
	if started || resolv != nil {
		t.Fatalf("expected no lazy start after Close, started=%v resolv=%v", started, resolv)
	}
	if got := len(e.jobs); got != 0 {
		t.Fatalf("expected empty queue after Close, got %d", got)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("expected 0 HTTP hits after Close, got %d", got)
	}
}

func TestEngineConcurrentEnqueueAndClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for iter := 0; iter < 10; iter++ {
		e := New(nil, nil, 2)
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(id int64) {
				defer wg.Done()
				e.ProbeSite(id, srv.URL)
			}(int64(g + 1))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.Close()
		}()
		wg.Wait()
		e.Close()
	}
}
