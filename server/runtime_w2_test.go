package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/norma/llm"
)

func TestSleepCtxStopsTimerOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(15 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if !sleepCtx(ctx, 10*time.Second) {
		t.Fatal("expected sleepCtx to report done=true when context cancels")
	}
	if time.Since(start) >= 2*time.Second {
		t.Fatal("sleepCtx did not exit promptly on cancel")
	}
	if sleepCtx(context.Background(), 5*time.Millisecond) {
		t.Fatal("expected sleepCtx to report done=false after timer expires")
	}
	if !sleepCtx(ctx, 0) {
		t.Fatal("expected sleepCtx with d<=0 and cancelled ctx to return done=true")
	}
}

type emptyFrontierDriver struct {
	frontierCalls atomic.Int64
}

func (d *emptyFrontierDriver) Open(string) (driver.Conn, error) {
	return &emptyFrontierConn{d: d}, nil
}
func (d *emptyFrontierDriver) Connect(context.Context) (driver.Conn, error) { return d.Open("") }
func (d *emptyFrontierDriver) Driver() driver.Driver                        { return d }

type emptyFrontierConn struct{ d *emptyFrontierDriver }

func (c *emptyFrontierConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *emptyFrontierConn) Close() error                        { return nil }
func (c *emptyFrontierConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c *emptyFrontierConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, "kind='intent' AND state='open'") {
		c.d.frontierCalls.Add(1)
	}
	return &notifierFakeRows{}, nil
}

func TestEngineTerminalWorkerParksWithoutPollingAndWakesOnRevival(t *testing.T) {
	d := &emptyFrontierDriver{}
	sd := sql.OpenDB(d)
	defer sd.Close()
	pg := &db.DB{DB: sd}

	task := &Task{
		ID:     "7",
		ExpID:  7,
		Status: "done",
		Store:  pg.Exploration(7),
		Guard:  guard.New(),
		notify: make(chan struct{}, 1),
	}
	m := &Manager{dir: t.TempDir(), pg: pg, tasks: map[string]*Task{task.ID: task}}
	e := NewEngine(m)

	var resolveCalls atomic.Int64
	p := new(terminalAdmissionProvider)
	w := agent.NewWorker(p, "fake", m.dir, nil, 200000, 2)
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		resolveCalls.Add(1)
		return nil, w
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		e.StopTask(task.ID)
	}()

	e.Run(ctx, task)
	time.Sleep(150 * time.Millisecond)

	if got := resolveCalls.Load(); got != 0 {
		t.Fatalf("terminal task workers polled resolver %d times, want 0", got)
	}
	if got := d.frontierCalls.Load(); got != 0 {
		t.Fatalf("terminal task workers polled Frontier %d times, want 0", got)
	}

	// Revive task from terminal state and verify parked workers wake up and check Frontier.
	task.lifecycleMu.Lock()
	task.Status = "running"
	task.lifecycleMu.Unlock()
	e.Run(ctx, task)
	e.Resume(task)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if resolveCalls.Load() > 0 && d.frontierCalls.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if resolveCalls.Load() == 0 || d.frontierCalls.Load() == 0 {
		t.Fatalf("parked terminal workers did not wake up after Run/Resume revival (resolve=%d, frontier=%d)",
			resolveCalls.Load(), d.frontierCalls.Load())
	}
}

type streamTestDriver struct {
	hash string
}

func (d *streamTestDriver) Open(string) (driver.Conn, error) { return &streamTestConn{d: d}, nil }
func (d *streamTestDriver) Connect(context.Context) (driver.Conn, error) {
	return d.Open("")
}
func (d *streamTestDriver) Driver() driver.Driver { return d }

type streamTestConn struct{ d *streamTestDriver }

func (c *streamTestConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *streamTestConn) Close() error                        { return nil }
func (c *streamTestConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c *streamTestConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, "FROM settings WHERE key=$1") {
		return &notifierFakeRows{rows: [][]driver.Value{{c.d.hash}}}, nil
	}
	if strings.Contains(q, "FROM activity") {
		return &notifierFakeRows{}, nil
	}
	return &notifierFakeRows{}, nil
}

func TestStreamHandlersExitPromptlyOnServerContextCancel(t *testing.T) {
	const hash = "stream-test-hash"
	sd := sql.OpenDB(&streamTestDriver{hash: hash})
	defer sd.Close()
	pg := &db.DB{DB: sd}

	task := &Task{
		ID:     "42",
		ExpID:  42,
		Status: "running",
		Store:  pg.Exploration(42),
		Guard:  guard.New(),
		notify: make(chan struct{}, 1),
	}
	m := &Manager{dir: t.TempDir(), pg: pg, tasks: map[string]*Task{task.ID: task}}
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	s := &Server{
		m:      m,
		ctx:    sctx,
		engine: NewEngine(m),
		jwtKey: []byte("stream-test-signing-key-32-bytes!"),
	}
	token, err := s.signAdminJWT(hash)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /logs", s.streamLogs)
	mux.HandleFunc("GET /activity", s.streamActivity)
	mux.HandleFunc("GET /update", s.updateStream)

	httpSrv := httptest.NewUnstartedServer(mux)
	httpSrv.Start()
	defer httpSrv.Close()

	paths := []string{"/logs", "/activity?task=" + task.ID, "/update"}
	done := make(chan error, len(paths))
	for _, p := range paths {
		req, _ := http.NewRequest(http.MethodGet, httpSrv.URL+p, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := httpSrv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
			resp.Body.Close()
			t.Fatalf("%s: unexpected status=%d content-type=%q", p, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		go func(body io.ReadCloser) {
			defer body.Close()
			_, err := io.ReadAll(body)
			done <- err
		}(resp.Body)
	}

	scancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Config.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("httpSrv.Shutdown blocked after server ctx cancel: %v", err)
	}
	for range paths {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("SSE stream read error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("SSE handler did not exit promptly after server context cancel")
		}
	}
}

func TestServerShutdownWaitsForArchiveAndSideRuns(t *testing.T) {
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()

	runDone := make(chan struct{})
	s := &Server{
		ctx: sctx,
		side: &sideQuestionState{
			runs: map[string]sideRun{
				"r1": {done: runDone},
			},
			done: make(chan struct{}),
		},
	}
	s.archiveWG.Add(1)

	shortCtx, shortCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer shortCancel()
	if err := s.Shutdown(shortCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded while archiveWG active, got %v", err)
	}

	s.archiveWG.Done()
	shortCtx2, shortCancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer shortCancel2()
	if err := s.Shutdown(shortCtx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded while side run active, got %v", err)
	}

	scancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond)
		close(runDone)
		close(s.side.done)
	}()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	if err := s.Shutdown(waitCtx); err != nil {
		t.Fatalf("Shutdown failed after workers settled: %v", err)
	}
	wg.Wait()
}

var _ llm.Provider = (*terminalAdmissionProvider)(nil)
