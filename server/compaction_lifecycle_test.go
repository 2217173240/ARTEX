package server

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

type lifecycleCompactorProvider struct {
	started chan context.Context
	release chan struct{}
	once    sync.Once
	body    string
}

func (p *lifecycleCompactorProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("compaction must use Complete"))
	}
}

func (p *lifecycleCompactorProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.started <- ctx
	// Deliberately keep the call alive after cancellation, like a provider that
	// still has response/usage cleanup to do before it returns.
	<-p.release
	if p.body != "" {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(p.body)}}, "end_turn", llm.Usage{}, nil
	}
	return llm.Message{}, "", llm.Usage{}, errors.New("controlled completion failure")
}

func (p *lifecycleCompactorProvider) unblock() { p.once.Do(func() { close(p.release) }) }

func compactorLifecycleFixture(t *testing.T) (*Server, *db.ExplorationStore, string, *lifecycleCompactorProvider, *db.DB) {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config: %v", err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	task, err := d.CreateTaskWithOptions("compactor lifecycle", "controlled fixture", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	var prev int64
	for i := 0; i < 20; i++ {
		id, err := ts.AddNode(db.KindFact, map[string]any{"summary": fmt.Sprintf("fact %d", i)}, 0, "confirmed", "test", nil)
		if err != nil {
			t.Fatal(err)
		}
		if prev != 0 {
			if err := ts.Link(prev, db.RelDerivedFrom, id); err != nil {
				t.Fatal(err)
			}
		}
		prev = id
	}
	if _, err := d.Exec(`UPDATE explorations SET round_no=7 WHERE id=$1`, ts.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET cold_since_round=1 WHERE exploration_id=$1 AND kind='fact'`, ts.ID()); err != nil {
		t.Fatal(err)
	}
	p := &lifecycleCompactorProvider{started: make(chan context.Context, 2), release: make(chan struct{})}
	t.Cleanup(p.unblock)
	s := &Server{engine: NewEngine(nil)}
	t.Cleanup(func() {
		p.unblock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.waitTaskQuiescent(ctx, strconv.FormatInt(task.ID, 10)); err != nil {
			t.Errorf("background compaction did not finish during cleanup: %v", err)
		}
	})
	return s, ts, strconv.FormatInt(task.ID, 10), p, d
}

func startLifecycleCompactor(t *testing.T, s *Server, ts *db.ExplorationStore, taskID string, p llm.Provider) {
	t.Helper()
	// Match the engine's planner round ownership: OnPlannerRound launches its
	// background fold, then the planner operation returns and releases its count.
	if !s.engine.beginTaskOperation(taskID) {
		t.Fatal("planner round was not admitted")
	}
	c := agent.NewCompactor(p, "fake")
	c.SetTaskAdmission(func() (func(), bool) { return s.engine.beginTaskCompaction(taskID) })
	c.OnPlannerRound(s.engine.execContextFor(context.Background(), taskID), ts)
	s.engine.decInflight(taskID)
}

func awaitCompactorProvider(t *testing.T, p *lifecycleCompactorProvider) context.Context {
	t.Helper()
	select {
	case ctx := <-p.started:
		return ctx
	case <-time.After(time.Second):
		t.Fatal("compactor did not start its fake provider")
		return nil
	}
}

func TestCompactorTaskDrainWaitsForComplete(t *testing.T) {
	s, ts, taskID, p, _ := compactorLifecycleFixture(t)
	startLifecycleCompactor(t, s, ts, taskID, p)
	awaitCompactorProvider(t, p)
	if !s.engine.BeginDelete(taskID) {
		t.Fatal("delete barrier was not installed")
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := s.waitTaskQuiescent(drainCtx, taskID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain returned while background Complete was blocked: %v", err)
	}
	p.unblock()
	doneCtx, doneCancel := context.WithTimeout(context.Background(), time.Second)
	defer doneCancel()
	if err := s.waitTaskQuiescent(doneCtx, taskID); err != nil {
		t.Fatalf("compactor did not release its task operation: %v", err)
	}
}

func TestCompactorTaskCancellationReachesProvider(t *testing.T) {
	s, ts, taskID, p, _ := compactorLifecycleFixture(t)
	startLifecycleCompactor(t, s, ts, taskID, p)
	providerCtx := awaitCompactorProvider(t, p)
	s.engine.BeginDelete(taskID)
	select {
	case <-providerCtx.Done():
		if !errors.Is(context.Cause(providerCtx), agent.AbortTaskDeleted) {
			t.Fatalf("cancellation cause=%v", context.Cause(providerCtx))
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("task deletion did not cancel background compaction")
	}
}

func TestCompactorReplacementCannotFoldSameTaskConcurrently(t *testing.T) {
	s, ts, taskID, p, _ := compactorLifecycleFixture(t)
	startLifecycleCompactor(t, s, ts, taskID, p)
	awaitCompactorProvider(t, p)
	// A replacement bundle gets a fresh Compactor after invalidateTaskAgents.
	startLifecycleCompactor(t, s, ts, taskID, p)
	select {
	case <-p.started:
		t.Fatal("replacement Compactor started a second fold for the same task")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCompactorDrainWaitsForDigestCommit(t *testing.T) {
	s, ts, taskID, p, d := compactorLifecycleFixture(t)
	p.body = "folded facts"
	// Hold the final INSERT after Complete has returned. This catches a gate
	// scoped only to the provider call instead of the whole asynchronous fold.
	lockKey := 1_000_000 + ts.ID()
	lockConn, err := d.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(context.Background(), `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		t.Fatal(err)
	}
	defer lockConn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockKey)
	name := fmt.Sprintf("compactor_commit_%d", ts.ID())
	if _, err := d.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.exploration_id=%d AND NEW.kind='digest' THEN
				PERFORM pg_advisory_xact_lock(%d);
			END IF;
			RETURN NEW;
		END $$`, name, ts.ID(), lockKey)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(fmt.Sprintf(`DROP FUNCTION %s() CASCADE`, name)) })
	if _, err := d.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON exploration_nodes FOR EACH ROW EXECUTE FUNCTION %s()`, name, name)); err != nil {
		t.Fatal(err)
	}
	startLifecycleCompactor(t, s, ts, taskID, p)
	awaitCompactorProvider(t, p)
	p.unblock()
	deadline := time.Now().Add(time.Second)
	for {
		var waiting bool
		if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=0 AND objid=$1 AND NOT granted)`, lockKey).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("digest INSERT did not reach the controlled commit barrier")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.engine.BeginDelete(taskID)
	drainCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := s.waitTaskQuiescent(drainCtx, taskID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain returned before digest transaction completed: %v", err)
	}
	if _, err := lockConn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockKey); err != nil {
		t.Fatal(err)
	}
	doneCtx, doneCancel := context.WithTimeout(context.Background(), time.Second)
	defer doneCancel()
	if err := s.waitTaskQuiescent(doneCtx, taskID); err != nil {
		t.Fatalf("digest completion did not release task admission: %v", err)
	}
	digests, err := ts.ActiveDigests()
	if err != nil || len(digests) != 1 {
		t.Fatalf("expected committed digest before quiescence: digests=%v err=%v", digests, err)
	}
}

func TestCompactorClosedTaskAdmissionDoesNotStartFold(t *testing.T) {
	s, ts, taskID, p, _ := compactorLifecycleFixture(t)
	s.engine.BeginDelete(taskID)
	c := agent.NewCompactor(p, "fake")
	c.SetTaskAdmission(func() (func(), bool) { return s.engine.beginTaskCompaction(taskID) })
	c.OnPlannerRound(context.Background(), ts)
	select {
	case <-p.started:
		t.Fatal("closed task admission started background Complete")
	case <-time.After(40 * time.Millisecond):
	}
	if got := s.engine.inflightCount(taskID); got != 0 {
		t.Fatalf("rejected fold leaked admission: inflight=%d", got)
	}
}
