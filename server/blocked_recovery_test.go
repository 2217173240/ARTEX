package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func recoveryFixture(t *testing.T) (*Manager, *Server) {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	t.Cleanup(restoreConcurrencySetting(t, m))
	if err := m.SetConcurrency(false, defaultConcurrencyLimit); err != nil {
		t.Fatal(err)
	}
	return m, newAdmissionTestServer(m, nil)
}

func recoveryTask(t *testing.T, m *Manager, s *Server, name string) (*Task, int64) {
	t.Helper()
	task, err := m.CreateTask(name, "synthetic recovery fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) })
	id, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "synthetic intent"}, 1, "blocked", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetIntentBlockedReason(id, "model_error: fixture EOF"); err != nil {
		t.Fatal(err)
	}
	s.engine.started.Store(task.ID, true)
	return task, id
}

func TestGlobalBlockedRecoveryOnlyRunningTasks(t *testing.T) {
	m, s := recoveryFixture(t)
	running, runningIntent := recoveryTask(t, m, s, "running")
	paused, pausedIntent := recoveryTask(t, m, s, "paused")
	if _, err := s.applyTaskControl(paused, "pause"); err != nil {
		t.Fatal(err)
	}
	terminalIDs := map[*Task]int64{}
	for _, status := range []string{"done", "failed", "timeout"} {
		task, id := recoveryTask(t, m, s, status)
		if err := m.SetTaskStatus(task.ID, status); err != nil {
			t.Fatal(err)
		}
		terminalIDs[task] = id
	}
	queued, queuedIntent := recoveryTask(t, m, s, "queued")
	if err := m.ApplyTaskAdmission(queued.ID, queued.Status, queued.Status, true, "resume", false); err != nil {
		t.Fatal(err)
	}
	s.engine.Pause(queued.ID, agent.AbortPausedByUser)
	request := httptest.NewRequest(http.MethodPost, "/api/tasks/rerun-blocked-all", nil)
	response := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/tasks/rerun-blocked-all", s.rerunBlockedAll)
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result blockedRecoveryResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Reopened != 1 || len(result.Items) != 1 || result.Items[0].ID != running.ID || result.Items[0].Error != "" {
		t.Fatalf("result=%+v", result)
	}
	node, _ := running.Store.GetNode(runningIntent)
	if node.State != "open" || node.BlockedReason != "" {
		t.Fatalf("running intent=%+v", node)
	}
	terminalIDs[paused], terminalIDs[queued] = pausedIntent, queuedIntent
	for task, id := range terminalIDs {
		node, err := task.Store.GetNode(id)
		if err != nil || node.State != "blocked" || node.BlockedReason != "model_error: fixture EOF" {
			t.Fatalf("excluded task %s intent=%+v err=%v", task.ID, node, err)
		}
	}
	// Repeating the explicit action reports no reopened work without waking inactive tasks.
	resultItem, eligible := s.recoverRunningBlocked(running)
	if !eligible || resultItem.Reopened != 0 || resultItem.Error != "" {
		t.Fatalf("repeat=%+v eligible=%v", resultItem, eligible)
	}
}

func TestGlobalBlockedRecoveryTerminalAdmissionRaceDoesNotResume(t *testing.T) {
	m, s := recoveryFixture(t)
	task, intentID := recoveryTask(t, m, s, "terminal race")
	if err := m.SetConcurrency(true, 1); err != nil {
		t.Fatal(err)
	}
	s.engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return nil, nil })
	task.setLLMState(nil, nil, nil, 1, "chain_exhausted", "fixture outage")
	old := s.engine.execContextFor(context.Background(), task.ID)
	s.engine.cancelExec(task.ID, agent.Causef("task_done", "fixture terminal", "fixture terminal"))
	if old.Err() == nil {
		t.Fatal("fixture execution was not cancelled")
	}
	// Commit the racing terminal row while deliberately retaining the stale live
	// handle. Admission's persisted compare-and-set must reject it, with no Resume.
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	if err := m.pg.SetStatus(id, "done"); err != nil {
		t.Fatal(err)
	}
	item, eligible := s.recoverRunningBlocked(task)
	if !eligible || item.Error == "" || item.Reopened != 0 {
		t.Fatalf("result=%+v eligible=%v", item, eligible)
	}
	node, err := task.Store.GetNode(intentID)
	if err != nil || node.State != "blocked" || node.BlockedReason != "model_error: fixture EOF" {
		t.Fatalf("rollback node=%+v err=%v", node, err)
	}
	fresh := s.engine.execContextFor(context.Background(), task.ID)
	if fresh.Err() == nil {
		t.Fatal("failed running-only admission resumed a cancelled execution")
	}
	stored, err := m.pg.GetTask(id)
	if err != nil || stored.Status != "done" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestGlobalBlockedRecoveryExcludesInactiveBeforeStoreAccess(t *testing.T) {
	for _, status := range []string{"paused", "done", "failed", "timeout", "queued", "created"} {
		t.Run(status, func(t *testing.T) {
			task := &Task{ID: "7", Status: "running", notify: make(chan struct{}, 1)}
			switch status {
			case "paused":
				task.Paused = true
			case "queued":
				task.Queued = true
			case "done", "failed", "timeout":
				task.Status = status
			}
			m := &Manager{tasks: map[string]*Task{task.ID: task}}
			s := newAdmissionTestServer(m, nil)
			if status != "created" {
				s.engine.started.Store(task.ID, true)
			}
			original := s.engine.execContextFor(context.Background(), task.ID)
			s.engine.cancelExec(task.ID, agent.Causef("fixture_cancel", "fixture", "fixture"))
			// Store is intentionally nil: excluded tasks must not read or mutate intents.
			item, eligible := s.recoverRunningBlocked(task)
			if eligible || item.Reopened != 0 {
				t.Fatalf("inactive recovery=%+v eligible=%v", item, eligible)
			}
			if fresh := s.engine.execContextFor(context.Background(), task.ID); fresh != original || fresh.Err() == nil {
				t.Fatal("inactive recovery cleared execution cancellation")
			}
		})
	}
}
