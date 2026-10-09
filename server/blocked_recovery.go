package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/db"
)

type blockedRecoveryItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Reopened int64  `json:"reopened"`
	Queued   bool   `json:"queued"`
	Error    string `json:"error,omitempty"`
}

type blockedRecoveryResult struct {
	Items    []blockedRecoveryItem `json:"items"`
	Reopened int64                 `json:"reopened"`
}

// rerunBlockedAll is an explicit recovery action over all running tasks, independent
// of list selection or filters. Each task reports its own result so a store failure
// does not prevent recovery of the other tasks.
func (s *Server) rerunBlockedAll(w http.ResponseWriter, r *http.Request) {
	result := blockedRecoveryResult{Items: make([]blockedRecoveryItem, 0)}
	for _, task := range s.m.List() {
		item, eligible := s.recoverRunningBlocked(task)
		if !eligible {
			continue
		}
		result.Items = append(result.Items, item)
		result.Reopened += item.Reopened
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) recoverRunningBlocked(t *Task) (blockedRecoveryItem, bool) {
	s.concMu.Lock()
	defer s.concMu.Unlock()
	item := blockedRecoveryItem{ID: t.ID, Name: t.lifecycleSnapshot().Name}
	current, exists := s.m.Task(t.ID)
	if !exists || current != t || s.resolvedTaskStatus(t) != "running" {
		return item, false
	}
	if !s.engine.beginTaskOperation(t.ID) {
		item.Error = "task is being deleted"
		return item, true
	}
	defer s.engine.decInflight(t.ID)
	intents, err := t.Store.ListByKind(db.KindIntent, 1000000)
	if err != nil {
		item.Error = err.Error()
		return item, true
	}
	before := make([]*db.Node, 0)
	for _, intent := range intents {
		if intent.State == "blocked" {
			before = append(before, intent)
		}
	}
	// Recheck after reading the store: a terminal writer does not take concMu.
	if s.resolvedTaskStatus(t) != "running" {
		return item, false
	}
	n, err := t.Store.ReopenBlockedIntents()
	if err != nil {
		item.Error = err.Error()
		return item, true
	}
	if n == 0 {
		return item, true
	}
	queued, err := s.admitTaskLocked(t, "resume", false, true)
	if err != nil {
		errors := []string{err.Error()}
		for _, intent := range before {
			if rollbackErr := restoreRerunIntent(t, intent); rollbackErr != nil {
				errors = append(errors, fmt.Sprintf("restore intent %d: %v", intent.ID, rollbackErr))
			}
		}
		item.Error = strings.Join(errors, "; ")
		return item, true
	}
	item.Reopened, item.Queued = n, queued
	return item, true
}
