package server

import (
	"sync"
	"sync/atomic"
)

// beginTaskCompaction admits the complete background fold as a task-owned
// writer and keeps it exclusive even when the task's agent bundle is replaced.
// The delete barrier and inflight registration share the same lock, so a task
// cannot be declared quiescent between admitting a fold and starting it.
func (e *Engine) beginTaskCompaction(taskID string) (func(), bool) {
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	if e.IsDeleting(taskID) || e.isSettling(taskID) {
		return nil, false
	}
	token := new(bool)
	if _, running := e.compacting.LoadOrStore(taskID, token); running {
		return nil, false
	}
	counter := e.inflightCounter(taskID)
	atomic.AddInt64(counter, 1)
	var once sync.Once
	return func() {
		once.Do(func() {
			e.compacting.CompareAndDelete(taskID, token)
			atomic.AddInt64(counter, -1)
		})
	}, true
}
