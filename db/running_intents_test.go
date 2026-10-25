package db

import (
	"os"
	"testing"
)

func runningIntentsTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("ARTEX_PG_DSN is required for PostgreSQL integration tests")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatalf("open configured PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func runningIntentsTestStore(t *testing.T, d *DB) *ExplorationStore {
	t.Helper()
	id, err := d.CreateExploration("running intents test", "bounded overview")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Exec(`DELETE FROM explorations WHERE id=$1`, id); err != nil {
			t.Errorf("clean exploration: %v", err)
		}
	})
	return d.Exploration(id)
}

func TestRunningIntentsFiltersBeforeLimit(t *testing.T) {
	d := runningIntentsTestDB(t)
	s := runningIntentsTestStore(t, d)
	other := runningIntentsTestStore(t, d)
	id, err := s.AddNode("intent", map[string]any{"summary": "old running"}, 7, "running", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET owner='worker-1', blocked_reason='reason', delete_reason='audit' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO exploration_nodes(exploration_id, kind, payload, state)
SELECT $1, 'intent', '{}'::jsonb, 'done' FROM generate_series(1, 350)`, s.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNode("fact", map[string]any{}, 0, "confirmed", "worker", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := other.AddNode("intent", map[string]any{}, 0, "running", "planner", nil); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{0, -1, 1, 300} {
		nodes, total, err := s.RunningIntents(limit)
		if err != nil || total != 1 || len(nodes) != 1 || nodes[0].ID != id {
			t.Fatalf("limit %d: nodes=%+v total=%d err=%v", limit, nodes, total, err)
		}
		n := nodes[0]
		if n.Kind != "intent" || n.State != "running" || n.Priority != 7 || n.Origin != "planner" || n.Owner != "worker-1" || n.BlockedReason != "reason" || n.DeleteReason != "audit" || n.CreatedAt.IsZero() || string(n.Payload) != `{"summary": "old running"}` {
			t.Fatalf("node fields were not preserved: %+v payload=%s", n, n.Payload)
		}
	}
}

func TestRunningIntentsBoundedCountAndOrder(t *testing.T) {
	d := runningIntentsTestDB(t)
	s := runningIntentsTestStore(t, d)
	if _, err := d.Exec(`INSERT INTO exploration_nodes(exploration_id, kind, payload, state)
SELECT $1, 'intent', '{}'::jsonb, 'running' FROM generate_series(1, 325)`, s.ID()); err != nil {
		t.Fatal(err)
	}
	var latest int64
	if err := d.QueryRow(`SELECT MAX(id) FROM exploration_nodes WHERE exploration_id=$1`, s.ID()).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ limit, length int }{{0, 300}, {-2, 300}, {7, 7}, {400, 325}} {
		nodes, total, err := s.RunningIntents(tc.limit)
		if err != nil || total != 325 || len(nodes) != tc.length {
			t.Fatalf("limit %d: length=%d total=%d err=%v", tc.limit, len(nodes), total, err)
		}
		if nodes[0].ID != latest {
			t.Fatalf("latest node=%d, want %d", nodes[0].ID, latest)
		}
		for i := 1; i < len(nodes); i++ {
			if nodes[i-1].ID <= nodes[i].ID {
				t.Fatalf("nodes are not ordered by descending ID at %d", i)
			}
		}
	}
}

func TestRunningIntentsEmpty(t *testing.T) {
	d := runningIntentsTestDB(t)
	s := runningIntentsTestStore(t, d)
	nodes, total, err := s.RunningIntents(10)
	if err != nil || nodes != nil || total != 0 {
		t.Fatalf("nodes=%+v total=%d err=%v", nodes, total, err)
	}
}

func TestRunningIntentsDatabaseError(t *testing.T) {
	d := runningIntentsTestDB(t)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	nodes, total, err := d.Exploration(1).RunningIntents(10)
	if err == nil || nodes != nil || total != 0 {
		t.Fatalf("closed database: nodes=%+v total=%d err=%v", nodes, total, err)
	}
}
