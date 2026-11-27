package server

import (
	"context"
	"encoding/json"
	"testing"
)

func TestListTasksReportsQueuedAndChainExhaustedRunning(t *testing.T) {
	t.Parallel()
	engine := NewEngine(nil)
	engine.started.Store("2", true)

	profileID := int64(11)
	tQueued := &Task{ID: "1", Description: "queued task", Goal: "g1", Status: "created", Queued: true}
	tExhausted := &Task{ID: "2", Description: "exhausted task", Goal: "g2", Status: "running"}
	tExhausted.setLLMState(&profileID, nil, []int64{profileID}, 1, "chain_exhausted", "quota exhausted")
	tPaused := &Task{ID: "3", Description: "paused task", Goal: "g3", Status: "paused", Paused: true}
	tDone := &Task{ID: "4", Description: "done task", Goal: "g4", Status: "done"}

	s := &Server{
		engine: engine,
		m: &Manager{
			tasks: map[string]*Task{
				"1": tQueued,
				"2": tExhausted,
				"3": tPaused,
				"4": tDone,
			},
		},
	}

	res, err := s.toolListTasks().Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("list_tasks err: %v", err)
	}
	if res.IsError {
		t.Fatalf("list_tasks returned tool error: %s", res.Flatten())
	}

	var rows []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(res.Flatten()), &rows); err != nil {
		t.Fatalf("unmarshal list_tasks output: %v\nraw=%s", err, res.Flatten())
	}
	got := make(map[string]string, len(rows))
	for _, r := range rows {
		got[r.ID] = r.Status
	}
	want := map[string]string{
		"1": "queued",
		"2": "running",
		"3": "paused",
		"4": "done",
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("task %s status=%q, want %q (all=%v)", id, got[id], w, got)
		}
	}
}
