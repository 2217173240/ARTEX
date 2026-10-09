package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmpool"
)

// This driver holds the first Save before it reaches storage. Later writes can
// finish first, reproducing the ordering of independent asynchronous SQL calls.
type llmHealthTestStore struct {
	mu           sync.Mutex
	rows         map[int64]db.LLMHealth
	saves        int
	firstStarted chan struct{}
	releaseFirst chan struct{}
	releaseOnce  sync.Once
	writes       chan struct{}
}

type llmHealthTestConnector struct{ store *llmHealthTestStore }
type llmHealthTestDriver struct{ store *llmHealthTestStore }
type llmHealthTestConn struct{ store *llmHealthTestStore }

func (c llmHealthTestConnector) Connect(context.Context) (driver.Conn, error) {
	return llmHealthTestConn{c.store}, nil
}
func (c llmHealthTestConnector) Driver() driver.Driver { return llmHealthTestDriver{c.store} }
func (d llmHealthTestDriver) Open(string) (driver.Conn, error) {
	return llmHealthTestConn{d.store}, nil
}
func (c llmHealthTestConn) Close() error { return nil }
func (c llmHealthTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected Prepare")
}
func (c llmHealthTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected Begin")
}

func (c llmHealthTestConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	s := c.store
	id := args[0].Value.(int64)
	switch {
	case strings.Contains(query, "INSERT INTO llm_profile_health"):
		s.mu.Lock()
		s.saves++
		first := s.saves == 1
		s.mu.Unlock()
		if first {
			close(s.firstStarted)
			select {
			case <-s.releaseFirst:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		h := db.LLMHealth{
			ProfileID: id, Fails: int(args[1].Value.(int64)), Trips: int(args[2].Value.(int64)),
			LastError: args[4].Value.(string), LastAt: time.Now(),
		}
		if value := args[3].Value; value != nil {
			until := value.(time.Time)
			h.OpenUntil = &until
		}
		s.mu.Lock()
		s.rows[id] = h
		s.mu.Unlock()
	case strings.Contains(query, "DELETE FROM llm_profile_health"):
		s.mu.Lock()
		delete(s.rows, id)
		s.mu.Unlock()
	default:
		return nil, fmt.Errorf("unexpected Exec: %s", query)
	}
	s.writes <- struct{}{}
	return driver.RowsAffected(1), nil
}

type llmHealthTestRows struct{ values [][]driver.Value }

func (r *llmHealthTestRows) Columns() []string {
	return []string{"profile_id", "fails", "trips", "open_until", "last_error", "last_at"}
}
func (r *llmHealthTestRows) Close() error { return nil }
func (r *llmHealthTestRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}
func (c llmHealthTestConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, "FROM llm_profile_health") {
		return nil, fmt.Errorf("unexpected Query: %s", query)
	}
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	rows := &llmHealthTestRows{}
	for _, h := range c.store.rows {
		if h.OpenUntil != nil && h.OpenUntil.After(time.Now()) {
			rows.values = append(rows.values, []driver.Value{
				h.ProfileID, int64(h.Fails), int64(h.Trips), *h.OpenUntil, h.LastError, h.LastAt,
			})
		}
	}
	return rows, nil
}

func newLLMHealthTestDB(t *testing.T) (*db.DB, *llmHealthTestStore) {
	t.Helper()
	store := &llmHealthTestStore{
		rows: make(map[int64]db.LLMHealth), firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}), writes: make(chan struct{}, 4),
	}
	pg := &db.DB{DB: sql.OpenDB(llmHealthTestConnector{store})}
	t.Cleanup(func() {
		store.releaseOnce.Do(func() { close(store.releaseFirst) })
		_ = pg.Close()
	})
	return pg, store
}

func waitLLMHealthTestSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for health persistence")
	}
}

func finishReorderedLLMHealthWrites(t *testing.T, store *llmHealthTestStore) {
	t.Helper()
	written := 0
	// An independent second write may complete before the blocked Save. With
	// serialization it stays pending; release the Save after that short window.
	select {
	case <-store.writes:
		written++
	case <-time.After(100 * time.Millisecond):
	}
	store.releaseOnce.Do(func() { close(store.releaseFirst) })
	for ; written < 2; written++ {
		waitLLMHealthTestSignal(t, store.writes)
	}
}

func TestLLMHealthPersistenceClearsLateSave(t *testing.T) {
	for _, recover := range []struct {
		name string
		run  func(*llmpool.Registry)
	}{
		{"Reset", func(reg *llmpool.Registry) { reg.Reset(1) }},
		{"Pass", func(reg *llmpool.Registry) { reg.Pass(1) }},
	} {
		t.Run(recover.name, func(t *testing.T) {
			pg, store := newLLMHealthTestDB(t)
			reg := newLLMHealthRegistry(pg)
			reg.Trip(1, "old failure", true)
			waitLLMHealthTestSignal(t, store.firstStarted)
			recover.run(reg)
			if reg.Get(1) != (llmpool.State{}) {
				t.Fatal("recovery must clear in-memory state without waiting for SQL")
			}
			finishReorderedLLMHealthWrites(t, store)
			if rows, err := pg.LoadLLMHealth(); err != nil || len(rows) != 0 {
				t.Fatalf("persisted health = %#v / %v, want no stale breaker", rows, err)
			}
			if restarted := newLLMHealthRegistry(pg); restarted.IsOpen(1) {
				t.Fatal("restart restored a breaker that was already recovered")
			}
		})
	}
}

func TestLLMHealthPersistenceKeepsLatestTrip(t *testing.T) {
	pg, store := newLLMHealthTestDB(t)
	reg := newLLMHealthRegistry(pg)
	reg.Trip(1, "old failure", true)
	waitLLMHealthTestSignal(t, store.firstStarted)
	reg.Trip(1, "latest failure", true)
	want := reg.Get(1)
	if want.Trips != 2 || want.LastError != "latest failure" {
		t.Fatalf("current state = %#v, want the second trip immediately", want)
	}
	finishReorderedLLMHealthWrites(t, store)
	restored := newLLMHealthRegistry(pg).Get(1)
	if restored.Trips != want.Trips || restored.LastError != want.LastError || !restored.OpenUntil.Equal(want.OpenUntil) {
		t.Fatalf("restored state = %#v, want latest trip %#v", restored, want)
	}
}
