package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
)

// This isolated database driver pauses the real Frontier query after workerLoop
// passed its lifecycle gates. All tool execution still runs through the actual
// runWorkerStep -> runIntent -> Worker.Execute -> harness call chain. No network,
// model credentials, or real task database is involved.
type terminalAdmissionDriver struct {
	arrived, release chan struct{}
	once             sync.Once
	settled          chan string
}

func (d *terminalAdmissionDriver) Open(string) (driver.Conn, error) {
	return &terminalAdmissionConn{d}, nil
}
func (d *terminalAdmissionDriver) Connect(context.Context) (driver.Conn, error) { return d.Open("") }
func (d *terminalAdmissionDriver) Driver() driver.Driver                        { return d }

type terminalAdmissionConn struct{ d *terminalAdmissionDriver }

func (c *terminalAdmissionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unused in isolated fake")
}
func (c *terminalAdmissionConn) Close() error { return nil }
func (c *terminalAdmissionConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unused in isolated fake")
}
func (c *terminalAdmissionConn) ExecContext(_ context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(q, "SET state=$1") && strings.Contains(q, "AND state=$4") {
		c.d.settled <- a[0].Value.(string)
	}
	return driver.RowsAffected(1), nil
}
func (c *terminalAdmissionConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	// These terminal-lifecycle fixtures have no calendar memberships. Support
	// only the scheduler's exact list query; unknown reads still fail loudly.
	if strings.HasPrefix(q, "SELECT id,name,enabled,type,timezone,run_date,end_date,weekdays,start_time,end_time,created_at,updated_at,manual_until,") && strings.HasSuffix(q, " FROM task_schedules ORDER BY id") {
		return &terminalAdmissionRows{values: make([]driver.Value, 14), done: true}, nil
	}
	if strings.Contains(q, "kind='intent' AND state='open'") && strings.HasPrefix(q, "SELECT id, kind, payload") {
		if c.d.arrived != nil {
			c.d.once.Do(func() { close(c.d.arrived); <-c.d.release })
		}
		return &terminalAdmissionRows{values: []driver.Value{int64(42), "intent", []byte(`{"summary":"harmless probe"}`), int64(1), "open", "planner", "", "", "", time.Now()}}, nil
	}
	if strings.Contains(q, "INSERT INTO activity") {
		return &terminalAdmissionRows{values: []driver.Value{int64(1)}}, nil
	}
	if strings.Contains(q, "RETURNING queued_at, queue_mode, completed_at, first_run_at, deadline_at") {
		return &terminalAdmissionRows{values: []driver.Value{nil, "", nil, nil, nil}}, nil
	}
	return nil, errors.New("read unavailable in isolated fake")
}

type terminalAdmissionRows struct {
	values []driver.Value
	done   bool
}

func (r *terminalAdmissionRows) Columns() []string { return make([]string, len(r.values)) }
func (r *terminalAdmissionRows) Close() error      { return nil }
func (r *terminalAdmissionRows) Next(v []driver.Value) error {
	if r.done {
		return io.EOF
	}
	copy(v, r.values)
	r.done = true
	return nil
}

type terminalAdmissionProvider struct {
	calls atomic.Int64
	// entered/release hold an already-admitted execution until its cancellation
	// and, optionally, explicit revival have both completed.
	entered, release chan struct{}
	cause            chan error
}

func (p *terminalAdmissionProvider) Stream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(y func(llm.StreamEvent, error) bool) {
		first := p.calls.Add(1) == 1
		if first && p.entered != nil {
			close(p.entered)
			<-p.release
			p.cause <- context.Cause(ctx)
			if ctx.Err() != nil {
				y(llm.StreamEvent{}, ctx.Err())
				return
			}
		}
		if ctx.Err() != nil {
			y(llm.StreamEvent{}, ctx.Err())
			return
		}
		if first {
			for _, e := range []llm.StreamEvent{
				{Type: llm.SEToolUseStart, ToolID: "probe", ToolName: "audit_probe"},
				{Type: llm.SEToolInputJSON, Text: `{}`},
				{Type: llm.SEMessageDelta, StopReason: "tool_use"},
				{Type: llm.SEMessageStop},
			} {
				if !y(e, nil) {
					return
				}
			}
		} else {
			y(llm.StreamEvent{Type: llm.SETextDelta, Text: "finished"}, nil)
			y(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
		}
	}
}
func (p *terminalAdmissionProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return llm.Message{}, "", llm.Usage{}, errors.New("unused in isolated fake")
}

func newTerminalAdmissionFixture(t *testing.T, d *terminalAdmissionDriver, p *terminalAdmissionProvider) (*Manager, *Engine, *Task, *agent.Worker, *atomic.Int64) {
	t.Helper()
	if d.settled == nil {
		d.settled = make(chan string, 16)
	}
	sd := sql.OpenDB(d)
	t.Cleanup(func() { _ = sd.Close() })
	pg := &db.DB{DB: sd}
	task := &Task{ID: "7", ExpID: 7, Status: "running", Store: pg.Exploration(7), Guard: guard.New(), notify: make(chan struct{}, 1)}
	m := &Manager{dir: t.TempDir(), pg: pg, tasks: map[string]*Task{task.ID: task}}
	e := NewEngine(m)
	probes := new(atomic.Int64)
	probe := tool.Build(tool.Spec{Name: "audit_probe", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
		probes.Add(1)
		return tool.Text("harmless memory probe"), nil
	}})
	w := agent.NewWorker(p, "fake", m.dir, nil, 200000, 2, probe)
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return nil, w })
	return m, e, task, w, probes
}

func awaitTerminalAdmission(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
func awaitTerminalIntentState(t *testing.T, d *terminalAdmissionDriver, want string) {
	t.Helper()
	select {
	case got := <-d.settled:
		if got != want {
			t.Errorf("intent settled as %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not settle its claimed intent")
	}
}

func TestWorkerRejectsExecutionAfterTerminalCommit(t *testing.T) {
	for _, status := range []string{"done", "failed", "timeout", "settling"} {
		for _, priorContext := range []bool{false, true} {
			t.Run(status+map[bool]string{false: "/first_context", true: "/existing_context"}[priorContext], func(t *testing.T) {
				d := &terminalAdmissionDriver{arrived: make(chan struct{}), release: make(chan struct{})}
				p := new(terminalAdmissionProvider)
				m, e, task, _, probes := newTerminalAdmissionFixture(t, d, p)
				var old context.Context
				if priorContext {
					old = e.execContextFor(context.Background(), task.ID)
				}
				ctx, cancel := context.WithCancel(context.Background())
				finished := make(chan struct{})
				go func() { defer close(finished); e.workerLoop(ctx, task, "work#1") }()
				t.Cleanup(func() { cancel(); awaitTerminalAdmission(t, finished, "worker shutdown") })
				awaitTerminalAdmission(t, d.arrived, "Frontier after lifecycle check")
				if status == "settling" {
					e.markSettling(task.ID)
					if old != nil && old.Err() != nil {
						t.Fatal("settlement cancelled the graceful drain context")
					}
				} else {
					if err := m.SetTaskStatus(task.ID, status); err != nil {
						t.Fatal(err)
					}
					e.cancelExec(task.ID, agent.AbortGoalMet)
					if old != nil && !errors.Is(context.Cause(old), agent.AbortGoalMet) {
						t.Fatal("terminal cancellation lost its initiating cause")
					}
				}
				close(d.release)
				awaitTerminalIntentState(t, d, "open")
				cancel()
				awaitTerminalAdmission(t, finished, "worker shutdown")
				if p.calls.Load() != 0 || probes.Load() != 0 {
					t.Fatalf("new execution after %s: model calls=%d harmless tool calls=%d", status, p.calls.Load(), probes.Load())
				}
				if e.inflightCount(task.ID) != 0 {
					t.Fatal("rejected execution leaked drain admission")
				}
			})
		}
	}
}

func TestTerminalExecutionContextNeedsExplicitRevival(t *testing.T) {
	for _, status := range []string{"done", "failed", "timeout"} {
		t.Run(status, func(t *testing.T) {
			m, e, task, worker, probes := newTerminalAdmissionFixture(t, new(terminalAdmissionDriver), new(terminalAdmissionProvider))
			if err := m.SetTaskStatus(task.ID, status); err != nil {
				t.Fatal(err)
			}
			old := e.execContextFor(context.Background(), task.ID)
			if old.Err() == nil {
				t.Fatal("terminal task without a previous context received a live context")
			}
			e.Resume(task)
			if e.execContextFor(context.Background(), task.ID).Err() == nil {
				t.Fatal("Resume revived a still-terminal task")
			}
			// Use the real task admission path, including its status commit and
			// timeout reset, while avoiding unrelated long-lived scheduler loops.
			e.started.Store(task.ID, true)
			s := &Server{m: m, engine: e, ctx: context.Background()}
			if queued, err := s.admitTask(task, "resume"); err != nil || queued {
				t.Fatalf("explicit revival: queued=%v err=%v", queued, err)
			}
			if fresh := e.execContextFor(context.Background(), task.ID); fresh == old || fresh.Err() != nil {
				t.Fatal("explicit admission did not create a fresh live context")
			}
			if !e.beginTaskOperation(task.ID) {
				t.Fatal("revived operation rejected")
			}
			e.runWorkerStep(context.Background(), task, "work#1", worker)
			e.decInflight(task.ID)
			if probes.Load() != 1 {
				t.Fatalf("revived worker tool calls=%d, want 1", probes.Load())
			}
		})
	}
}

func TestCancellationBeforeFirstContextClosesExecutionAdmission(t *testing.T) {
	e := NewEngine(nil)
	e.cancelExec("7", agent.AbortGoalMet)
	ctx := e.execContextFor(context.Background(), "7")
	if !errors.Is(context.Cause(ctx), agent.AbortGoalMet) {
		t.Fatalf("cause=%v, want goal_met", context.Cause(ctx))
	}
	if e.execContextFor(context.Background(), "7") != ctx {
		t.Fatal("context lookup silently renewed cancelled execution")
	}
	e.Resume(&Task{ID: "7", notify: make(chan struct{}, 1)})
	if e.execContextFor(context.Background(), "7").Err() != nil {
		t.Fatal("explicit Resume did not reopen execution")
	}
}

func TestTerminalCancellationRetainsInflightCauseAfterRevival(t *testing.T) {
	for _, mode := range []string{"terminal", "settling", "terminal_then_new_settlement"} {
		t.Run(mode, func(t *testing.T) {
			d := new(terminalAdmissionDriver)
			p := &terminalAdmissionProvider{entered: make(chan struct{}), release: make(chan struct{}), cause: make(chan error, 1)}
			m, e, task, _, probes := newTerminalAdmissionFixture(t, d, p)
			ctx, cancel := context.WithCancel(context.Background())
			finished := make(chan struct{})
			go func() { defer close(finished); e.workerLoop(ctx, task, "work#1") }()
			t.Cleanup(func() { cancel(); awaitTerminalAdmission(t, finished, "worker shutdown") })
			awaitTerminalAdmission(t, p.entered, "admitted model call")
			cause, state, status := agent.AbortGoalMet, "stopped", "done"
			if mode == "settling" {
				e.markSettling(task.ID)
				cause, state, status = agent.AbortSettleDrainTimeout, "exhausted", "timeout"
			}
			if err := m.SetTaskStatus(task.ID, status); err != nil {
				t.Fatal(err)
			}
			e.cancelExec(task.ID, cause)
			if e.inflightCount(task.ID) != 1 {
				t.Fatal("cancellation released operation before execution settled")
			}
			// Revive before the old provider returns: old cancellation must still
			// settle stopped/exhausted rather than blocked or replaying the intent.
			e.started.Store(task.ID, true)
			s := &Server{m: m, engine: e, ctx: context.Background()}
			if queued, err := s.admitTask(task, "resume"); err != nil || queued {
				t.Fatalf("explicit revival: queued=%v err=%v", queued, err)
			}
			if mode == "terminal_then_new_settlement" {
				e.markSettling(task.ID)
			}
			cancel() // prevent this test worker from claiming more fake frontier rows
			close(p.release)
			awaitTerminalIntentState(t, d, state)
			awaitTerminalAdmission(t, finished, "worker shutdown")
			if got := <-p.cause; !errors.Is(got, cause) {
				t.Fatalf("old cause=%v, want %v", got, cause)
			}
			if probes.Load() != 0 || p.calls.Load() != 1 {
				t.Fatalf("cancelled worker replayed or ran tool: calls=%d probes=%d", p.calls.Load(), probes.Load())
			}
			if e.inflightCount(task.ID) != 0 {
				t.Fatal("cancelled worker leaked drain admission")
			}
		})
	}
}

func TestAbortDeleteReopensExecutionWithoutRevivingOldContext(t *testing.T) {
	e := NewEngine(nil)
	old := e.execContextFor(context.Background(), "7")
	e.BeginDelete("7")
	e.AbortDelete("7", false)
	if fresh := e.execContextFor(context.Background(), "7"); fresh == old || fresh.Err() != nil {
		t.Fatal("failed delete compensation did not reopen execution")
	}
	if !errors.Is(context.Cause(old), agent.AbortTaskDeleted) {
		t.Fatal("delete compensation lost old run cancellation")
	}
}

func TestSettlingPreservesAdmittedWorkerDrain(t *testing.T) {
	d := new(terminalAdmissionDriver)
	p := &terminalAdmissionProvider{entered: make(chan struct{}), release: make(chan struct{}), cause: make(chan error, 1)}
	_, e, task, _, probes := newTerminalAdmissionFixture(t, d, p)
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { defer close(finished); e.workerLoop(ctx, task, "work#1") }()
	t.Cleanup(func() { cancel(); awaitTerminalAdmission(t, finished, "worker shutdown") })
	awaitTerminalAdmission(t, p.entered, "admitted model call")
	e.markSettling(task.ID)
	if e.execContextFor(context.Background(), task.ID).Err() == nil {
		t.Fatal("settlement admitted a new execution")
	}
	if e.inflightCount(task.ID) != 1 {
		t.Fatal("settlement lost the admitted worker's drain reservation")
	}
	close(p.release)
	awaitTerminalIntentState(t, d, "done")
	cancel()
	awaitTerminalAdmission(t, finished, "worker shutdown")
	if got := <-p.cause; got != nil {
		t.Fatalf("graceful settlement cancelled the admitted context: %v", got)
	}
	if probes.Load() != 1 || e.inflightCount(task.ID) != 0 {
		t.Fatalf("admitted worker failed to drain: probes=%d inflight=%d", probes.Load(), e.inflightCount(task.ID))
	}
}

func TestTerminalCommitBeforeCancellationPreservesInitiatingCause(t *testing.T) {
	m, e, task, _, _ := newTerminalAdmissionFixture(t, new(terminalAdmissionDriver), new(terminalAdmissionProvider))
	old := e.execContextFor(context.Background(), task.ID)
	if err := m.SetTaskStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	if e.execContextFor(context.Background(), task.ID).Err() == nil {
		t.Fatal("status commit admitted a new execution before cancellation")
	}
	if old.Err() != nil {
		t.Fatal("rejected admission overwrote cancellation of the existing execution")
	}
	e.cancelExec(task.ID, agent.AbortGoalMet)
	if !errors.Is(context.Cause(old), agent.AbortGoalMet) {
		t.Fatalf("initiating cause=%v, want goal_met", context.Cause(old))
	}
}
