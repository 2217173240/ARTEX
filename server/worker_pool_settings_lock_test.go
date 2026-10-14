package server

import (
	"context"
	"os"
	"testing"
	"time"
)

// Real PostgreSQL locks hold the configuration query in flight. This protects
// task teardown availability specifically while worker-count reads are blocked.
func TestWorkerPoolBlockedSettingsDoesNotBlockStopTask(t *testing.T) {
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
	e := NewEngine(m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := &taskRuntime{ctx: ctx, cancel: cancel}
	rt.pool = newWorkerPool(ctx, 4, func(ctx context.Context, _ int, _ *workerPool) { <-ctx.Done() })
	rt.wg.Add(1)
	runTaskRoutine(rt, func(ctx context.Context) { <-ctx.Done(); rt.pool.Close() })
	e.runtimes["synthetic"] = rt
	defer rt.pool.Close()
	tx, err := m.pg.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("LOCK TABLE settings IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	reconciled := make(chan error, 1)
	go func() { reconciled <- e.ReconcileWorkers() }()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := m.pg.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT value FROM settings WHERE key=%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("settings reader did not reach PostgreSQL lock")
		}
	}
	if !e.deleteMu.TryLock() {
		t.Fatal("blocked settings query holds task delete lock")
	}
	e.deleteMu.Unlock()
	stopped := make(chan struct{})
	go func() { e.StopTask("synthetic"); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("StopTask blocked behind settings SQL")
	}
	select {
	case <-reconciled:
		t.Fatal("configuration query unexpectedly finished before lock release")
	default:
	}
	e.runtimeMu.Lock()
	remaining := len(e.runtimes)
	e.runtimeMu.Unlock()
	if remaining != 0 {
		t.Fatal("StopTask left task runtime")
	}
	rt.pool.mu.Lock()
	left := len(rt.pool.workers)
	rt.pool.mu.Unlock()
	if left != 0 {
		t.Fatal("StopTask did not join dynamic workers")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-reconciled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("configuration query did not finish after lock release")
	}
}
