package server

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func caseReviewQueueFixture(t *testing.T) (*Server, context.CancelFunc, []int64) {
	t.Helper()
	// Queue tests must never fall back to the user's configured database.
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("explicit isolated ARTEX_PG_DSN required")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ctx: ctx, m: &Manager{pg: pg},
		chatBusy: map[string]bool{}, chatCancel: map[string]context.CancelCauseFunc{},
		triggerQ: map[string][]triggeredRun{}, triggerActive: map[string]int{}, triggerCfg: map[string]triggerBehavior{}}
	ids := []int64{}
	t.Cleanup(func() {
		cancel()
		waitCaseReviewQueueIdle(t, s)
		for _, id := range ids {
			_, _ = pg.Exec(`DELETE FROM conversations WHERE id=$1`, id)
		}
		pg.Close()
	})
	for range 3 {
		c, err := pg.CreateConversation("reporter", "queue fixture", nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
		if _, err := pg.Exec(`INSERT INTO finding_case_review_runs(conversation_id) VALUES($1)`, c.ID); err != nil {
			t.Fatal(err)
		}
		s.triggerQ["reporter"] = append(s.triggerQ["reporter"], triggeredRun{agentKey: "reporter", conversationID: c.ID, message: "synthetic saved evidence"})
	}
	s.triggerCfg["reporter"] = triggerBehavior{runMode: "serial", mergeMode: "all"}
	return s, cancel, ids
}

func waitCaseReviewQueueIdle(t *testing.T, s *Server) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		s.queueMu.Lock()
		active := s.triggerActive["reporter"]
		s.queueMu.Unlock()
		if active == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("reporter queue did not release its active slot")
		}
	}
}

func TestCaseReviewQueueFailureDrainsNextWithoutRetry(t *testing.T) {
	s, _, ids := caseReviewQueueFixture(t)
	// No model is configured: run the actual startup failure path, without a
	// provider or target. Each failed job must free the slot for the next one.
	s.queueMu.Lock()
	s.pumpLocked("reporter")
	s.queueMu.Unlock()
	waitCaseReviewQueueIdle(t, s)
	s.queueMu.Lock()
	remaining := len(s.triggerQ["reporter"])
	// Re-pumping an idle queue must not resurrect the terminal failures.
	s.pumpLocked("reporter")
	active := s.triggerActive["reporter"]
	s.queueMu.Unlock()
	if remaining != 0 || active != 0 {
		t.Fatalf("queue remaining=%d active=%d", remaining, active)
	}
	for _, id := range ids {
		var state, reason string
		var attempts int
		if err := s.m.pg.QueryRow(`SELECT state,error FROM finding_case_review_runs WHERE conversation_id=$1`, id).Scan(&state, &reason); err != nil {
			t.Fatal(err)
		}
		if err := s.m.pg.QueryRow(`SELECT count(*) FROM conversation_activities WHERE conversation_id=$1 AND kind='user'`, id).Scan(&attempts); err != nil {
			t.Fatal(err)
		}
		if state != "failed" || reason == "" || attempts != 1 {
			t.Fatalf("conversation %d: state=%q reason=%q attempts=%d", id, state, reason, attempts)
		}
	}
}

func TestCaseReviewQueueCancellationStopsDrain(t *testing.T) {
	s, cancel, ids := caseReviewQueueFixture(t)
	// Simulate shutdown after the head was dequeued but before its runner starts.
	s.queueMu.Lock()
	head := s.nextTriggerRun("reporter", s.triggerCfg["reporter"])
	s.triggerActive["reporter"]++
	s.queueMu.Unlock()
	cancel()
	s.runAndPump("reporter", head)
	var state, reason string
	if err := s.m.pg.QueryRow(`SELECT state,error FROM finding_case_review_runs WHERE conversation_id=$1`, ids[0]).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || reason != "整理已停止或服务关闭" {
		t.Fatalf("cancelled head state=%q reason=%q", state, reason)
	}
	s.queueMu.Lock()
	remaining, active := len(s.triggerQ["reporter"]), s.triggerActive["reporter"]
	s.queueMu.Unlock()
	if remaining != 2 || active != 0 {
		t.Fatalf("cancelled queue remaining=%d active=%d", remaining, active)
	}
	for _, id := range ids[1:] {
		var attempts int
		if err := s.m.pg.QueryRow(`SELECT count(*) FROM conversation_activities WHERE conversation_id=$1`, id).Scan(&attempts); err != nil {
			t.Fatal(err)
		}
		if attempts != 0 {
			t.Fatalf("queued conversation %d started after cancellation", id)
		}
	}
}
