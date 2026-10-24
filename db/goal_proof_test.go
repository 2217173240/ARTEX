package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func goalProofDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_GOAL_PROOF_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("ARTEX_PG_DSN")
	}
	if dsn == "" {
		t.Skip("ARTEX_GOAL_PROOF_TEST_DSN or ARTEX_PG_DSN must explicitly identify a test PostgreSQL database")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func proofTask(t *testing.T, d *DB, sources ...int64) *Task {
	t.Helper()
	task, err := d.CreateTaskWithOptions("goal proof test", "goal proof test", TaskCreateOptions{SourceTaskIDs: sources})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID); err != nil {
			t.Error(err)
		}
		if _, err := d.Exec(`DELETE FROM explorations WHERE id=$1`, task.ExplorationID); err != nil {
			t.Error(err)
		}
	})
	return task
}

func proofNode(t *testing.T, s *ExplorationStore, kind string) int64 {
	t.Helper()
	state := "open"
	if kind == KindFact || kind == KindFinding {
		state = "confirmed"
	}
	id, err := s.AddNode(kind, map[string]any{"summary": "proof test"}, 0, state, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGoalProofTransaction(t *testing.T) {
	d := goalProofDB(t)
	task := proofTask(t, d)
	s := d.Exploration(task.ExplorationID)
	goal, other, fact := proofNode(t, s, KindGoal), proofNode(t, s, KindGoal), proofNode(t, s, KindFact)
	if _, err := d.Exec(`UPDATE exploration_nodes SET blocked_reason='waiting' WHERE id=$1`, goal); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, err := s.ProveGoal(t.Context(), goal, fact)
		if err != nil || got.Total != 2 || got.Met != 1 || got.AllMet {
			t.Fatalf("repeat %d: %+v %v", i, got, err)
		}
	}
	var edges, version int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND rel='proves'`, s.expID).Scan(&edges); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT content_version FROM exploration_nodes WHERE id=$1`, goal).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if edges != 1 || version != 2 {
		t.Fatalf("edges=%d version=%d", edges, version)
	}
	var blocked bool
	if err := d.QueryRow(`SELECT blocked_reason IS NULL FROM exploration_nodes WHERE id=$1`, goal).Scan(&blocked); err != nil || !blocked {
		t.Fatalf("blocked_reason cleared=%v err=%v", blocked, err)
	}
	got, err := s.ProveGoal(t.Context(), other, fact)
	if err != nil || !got.AllMet || got.Total != 2 || got.Met != 2 {
		t.Fatalf("final: %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ProveGoal(ctx, goal, fact); err == nil {
		t.Fatal("canceled proof succeeded")
	}
}

func TestGoalProofConcurrent(t *testing.T) {
	d := goalProofDB(t)
	task := proofTask(t, d)
	s := d.Exploration(task.ExplorationID)
	goals := []int64{proofNode(t, s, KindGoal), proofNode(t, s, KindGoal)}
	fact := proofNode(t, s, KindFact)
	type outcome struct {
		result GoalProofResult
		err    error
	}
	results := make(chan outcome, 2)
	for _, goal := range goals {
		go func(id int64) { r, err := s.ProveGoal(t.Context(), id, fact); results <- outcome{r, err} }(goal)
	}
	allMet := 0
	for range goals {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.result.AllMet {
			allMet++
		}
	}
	if allMet != 1 {
		t.Fatalf("concurrent completed results=%d, want 1", allMet)
	}
}

func TestGoalProofCountsEveryGoal(t *testing.T) {
	d := goalProofDB(t)
	task := proofTask(t, d)
	s := d.Exploration(task.ExplorationID)
	oldest := proofNode(t, s, KindGoal)
	fact := proofNode(t, s, KindFact)
	if _, err := d.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,state) SELECT $1,'goal','met' FROM generate_series(1,1000)`, s.expID); err != nil {
		t.Fatal(err)
	}
	goal := proofNode(t, s, KindGoal)
	got, err := s.ProveGoal(t.Context(), goal, fact)
	if err != nil || got.Total != 1002 || got.Met != 1001 || got.AllMet {
		t.Fatalf("oldest=%d result=%+v err=%v", oldest, got, err)
	}
}

func TestGoalProofSourceBoundary(t *testing.T) {
	d := goalProofDB(t)
	grand := proofTask(t, d)
	source := proofTask(t, d, grand.ID)
	current := proofTask(t, d, source.ID)
	foreign := proofTask(t, d)
	s := d.Exploration(current.ExplorationID)
	goal := proofNode(t, s, KindGoal)
	for _, kind := range []string{KindFact, KindFinding} {
		id := proofNode(t, d.Exploration(source.ExplorationID), kind)
		if _, err := s.ProveGoal(t.Context(), goal, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{proofNode(t, d.Exploration(grand.ExplorationID), KindFact), proofNode(t, d.Exploration(foreign.ExplorationID), KindFact), proofNode(t, s, KindIntent), goal, -1} {
		if _, err := s.ProveGoal(t.Context(), goal, id); err == nil {
			t.Fatalf("accepted evidence %d", id)
		}
	}
	fact := proofNode(t, s, KindFact)
	for _, id := range []int64{fact, proofNode(t, d.Exploration(source.ExplorationID), KindGoal), -1} {
		if _, err := s.ProveGoal(t.Context(), id, fact); err == nil {
			t.Fatalf("accepted goal %d", id)
		}
	}
	if _, err := d.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	sourceFact := proofNode(t, d.Exploration(source.ExplorationID), KindFact)
	if _, err := s.ProveGoal(t.Context(), goal, sourceFact); err == nil {
		t.Fatal("accepted deleted source")
	}
	if _, err := d.Exec(`UPDATE tasks SET deleted_at=NULL WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, current.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProveGoal(t.Context(), goal, sourceFact); err == nil {
		t.Fatal("accepted inherited evidence for deleted owner")
	}
}

func TestGoalProofWriteFailuresRollback(t *testing.T) {
	d := goalProofDB(t)
	for _, stage := range []string{"edge", "goal", "zero_rows", "commit"} {
		t.Run(stage, func(t *testing.T) {
			task := proofTask(t, d)
			s := d.Exploration(task.ExplorationID)
			goal := proofNode(t, s, KindGoal)
			fact := proofNode(t, s, KindFact)
			name := fmt.Sprintf("goal_proof_%s_%d", stage, goal)
			table, event, condition := "exploration_edges", "BEFORE INSERT", fmt.Sprintf("NEW.exploration_id=%d", s.expID)
			body := "RAISE EXCEPTION 'goal proof injected failure';"
			if stage == "goal" || stage == "zero_rows" {
				table = "exploration_nodes"
				event = "BEFORE UPDATE"
				condition = fmt.Sprintf("NEW.id=%d", goal)
			}
			if stage == "zero_rows" {
				body = "RETURN NULL;"
			}
			if _, err := d.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF %s THEN %s END IF; RETURN NEW; END $$`, name, condition, body)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := d.Exec(fmt.Sprintf(`DROP FUNCTION %s() CASCADE`, name)); err != nil {
					t.Error(err)
				}
			})
			ddl := fmt.Sprintf(`CREATE TRIGGER %s %s ON %s FOR EACH ROW EXECUTE FUNCTION %s()`, name, event, table, name)
			if stage == "commit" {
				ddl = fmt.Sprintf(`CREATE CONSTRAINT TRIGGER %s AFTER INSERT ON %s DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION %s()`, name, table, name)
			}
			if _, err := d.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			_, err := s.ProveGoal(t.Context(), goal, fact)
			if err == nil {
				t.Fatal("injected database failure succeeded")
			}
			if stage == "commit" && !strings.Contains(err.Error(), "commit goal proof") {
				t.Fatalf("failure did not reach commit: %v", err)
			}
			var state string
			var edges, version int
			if err := d.QueryRow(`SELECT state,content_version FROM exploration_nodes WHERE id=$1`, goal).Scan(&state, &version); err != nil {
				t.Fatal(err)
			}
			if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1`, s.expID).Scan(&edges); err != nil {
				t.Fatal(err)
			}
			if state != "open" || edges != 0 || version != 0 {
				t.Fatalf("partial proof state=%s edges=%d version=%d", state, edges, version)
			}
		})
	}
}

func TestGoalProofCountFailureRollsBack(t *testing.T) {
	d := goalProofDB(t)
	d.SetMaxOpenConns(1)
	task := proofTask(t, d)
	s := d.Exploration(task.ExplorationID)
	goal, fact := proofNode(t, s, KindGoal), proofNode(t, s, KindFact)
	schema := fmt.Sprintf("goal_proof_count_%d", goal)
	if _, err := d.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Exec(`SET search_path TO public`); err != nil {
			t.Error(err)
		}
		if _, err := d.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Error(err)
		}
	})
	// 可更新视图在实际统计 SELECT 时抛出 PostgreSQL 错误。
	if _, err := d.Exec(`CREATE FUNCTION ` + schema + `.allow_read() RETURNS boolean LANGUAGE plpgsql VOLATILE AS $$
BEGIN IF current_query() LIKE 'SELECT count(*), count(*) FILTER%' THEN
RAISE EXCEPTION 'goal proof count failure'; END IF; RETURN true; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`CREATE VIEW ` + schema + `.exploration_nodes AS SELECT * FROM public.exploration_nodes WHERE ` + schema + `.allow_read()`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`SET search_path TO ` + schema + `,public`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProveGoal(t.Context(), goal, fact); err == nil || !strings.Contains(err.Error(), "count proved goals") {
		t.Fatalf("count failure: %v", err)
	}
	var state string
	var version, edges int
	if err := d.QueryRow(`SELECT state,content_version FROM public.exploration_nodes WHERE id=$1`, goal).Scan(&state, &version); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1`, s.expID).Scan(&edges); err != nil {
		t.Fatal(err)
	}
	if state != "open" || version != 0 || edges != 0 {
		t.Fatalf("partial proof state=%s version=%d edges=%d", state, version, edges)
	}
}

func TestGoalProofTaskDeletionLockOrder(t *testing.T) {
	d := goalProofDB(t)
	for _, target := range []string{"owner", "source"} {
		t.Run(target, func(t *testing.T) {
			source := proofTask(t, d)
			owner := proofTask(t, d, source.ID)
			s := d.Exploration(owner.ExplorationID)
			goal := proofNode(t, s, KindGoal)
			fact := proofNode(t, d.Exploration(source.ExplorationID), KindFact)
			deleted := owner
			if target == "source" {
				deleted = source
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			tx, err := d.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var pid, id int64
			if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if err := tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE id=$1 FOR UPDATE`, deleted.ID).Scan(&id); err != nil {
				t.Fatal(err)
			}
			proofDone := make(chan error, 1)
			go func() { _, err := s.ProveGoal(ctx, goal, fact); proofDone <- err }()
			// 数据库确认证明正在等待删除事务的任务锁，再执行删除的图阶段。
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				var blocked bool
				if err := d.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
WHERE $1=ANY(pg_blocking_pids(pid)) AND wait_event_type='Lock')`, pid).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case err := <-proofDone:
					t.Fatalf("proof finished before task lock wait: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout='500ms'`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id=$1`, deleted.ID); err != nil {
				t.Fatalf("delete task blocked by proof: %v", err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM explorations WHERE id=$1`, deleted.ExplorationID); err != nil {
				t.Fatalf("delete exploration blocked by proof: %v", err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := <-proofDone; err != nil {
				t.Fatalf("proof after deletion rollback: %v", err)
			}
		})
	}
}

func TestGoalProofWithoutTask(t *testing.T) {
	d := goalProofDB(t)
	expID, err := d.CreateExploration("proof without task", "proof without task")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Exec(`DELETE FROM explorations WHERE id=$1`, expID); err != nil {
			t.Error(err)
		}
	})
	s := d.Exploration(expID)
	goal, fact := proofNode(t, s, KindGoal), proofNode(t, s, KindFact)
	if got, err := s.ProveGoal(t.Context(), goal, fact); err != nil || !got.AllMet {
		t.Fatalf("proof without task: %+v %v", got, err)
	}
}
