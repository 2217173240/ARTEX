package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/sidequestion"
)

func checkpointServer(t *testing.T) *Server {
	t.Helper()
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("ARTEX_PG_DSN required for real PostgreSQL checkpoint tests")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pg.Close(); err != nil {
			t.Error(err)
		}
	})
	s := &Server{m: &Manager{pg: pg}, ctx: context.Background()}
	s.side = &sideQuestionState{pending: map[string]sidequestion.Snapshot{}, latest: map[string]sidequestion.Snapshot{}, seen: map[string][2]int64{}, retries: map[string]sideCheckpointRetry{}, runs: map[string]sideRun{}}
	return s
}

func checkpointConversation(t *testing.T, s *Server) sidequestion.Snapshot {
	t.Helper()
	c, err := s.m.pg.CreateConversation("mainagent", "checkpoint retry", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.m.pg.Exec(`DELETE FROM conversations WHERE id=$1`, c.ID); err != nil {
			t.Error(err)
		}
	})
	return sidequestion.Snapshot{Parent: sidequestion.Parent{ConversationID: c.ID}, RunID: 1, Version: 1}
}

func checkpointFailure(t *testing.T, s *Server, key string) (func() int64, func()) {
	t.Helper()
	name := fmt.Sprintf("checkpoint_%d", time.Now().UnixNano())
	exec := func(q string) {
		t.Helper()
		if _, err := s.m.pg.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE SEQUENCE ` + name)
	exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.session_key = '%s' THEN PERFORM nextval('%s'); RAISE EXCEPTION 'checkpoint test failure' USING ERRCODE='42501'; END IF; RETURN NEW; END $$`, name, key, name))
	exec(`CREATE TRIGGER ` + name + ` BEFORE INSERT OR UPDATE ON side_question_sessions FOR EACH ROW EXECUTE FUNCTION ` + name + `()`)
	removed := false
	repair := func() {
		if !removed {
			exec(`DROP TRIGGER ` + name + ` ON side_question_sessions`)
			removed = true
		}
	}
	t.Cleanup(func() { repair(); exec(`DROP FUNCTION ` + name + `()`); exec(`DROP SEQUENCE ` + name) })
	return func() int64 {
		var n int64
		var called bool
		if err := s.m.pg.QueryRow(`SELECT last_value,is_called FROM `+name).Scan(&n, &called); err != nil {
			t.Fatal(err)
		}
		if !called {
			return 0
		}
		return n
	}, repair
}

func TestCheckpointRetrySchedule(t *testing.T) {
	r := sideCheckpointRetry{}
	now := time.Now()
	for i, want := range []time.Duration{1, 2, 4, 8, 16, 30, 30, 30, 30} {
		var got time.Duration
		r, got = r.failed(now)
		if got != want*time.Second || !r.nextAttempt.Equal(now.Add(got)) || r.failures > 6 {
			t.Fatalf("step %d: %+v delay %v", i, r, got)
		}
	}
}

func TestCheckpointRetryCoalescesAndRepairs(t *testing.T) {
	s := checkpointServer(t)
	snap := checkpointConversation(t, s)
	key := snap.Parent.Key()
	attempts, repair := checkpointFailure(t, s, key)
	s.publishSideSnapshot(snap)
	s.flushSideSnapshotsMatching(false, nil)
	retry := s.side.retries[key]
	if attempts() != 1 || retry.failures != 1 {
		t.Fatal("first failure missing")
	}
	if err := s.m.pg.ClearSideHistory(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if s.side.retries[key] != retry || s.side.pending[key].Version != 1 {
		t.Fatal("history clear discarded pending context")
	}
	for _, identity := range [][2]int64{{1, 2}, {1, 3}, {2, 1}} {
		snap.RunID, snap.Version = identity[0], identity[1]
		s.publishSideSnapshot(snap)
		s.flushSideSnapshotsMatching(false, nil)
		if attempts() != 1 || s.side.retries[key] != retry {
			t.Fatal("publication bypassed retry delay")
		}
	}
	other := checkpointConversation(t, s)
	s.publishSideSnapshot(other)
	s.flushSideSnapshotsMatching(false, nil)
	if saved, err := s.m.pg.SideSnapshot(context.Background(), other.Parent.Key()); err != nil || saved == nil {
		t.Fatalf("healthy parent: %v %v", saved, err)
	}
	repair()
	deadline := time.Now().Add(3 * time.Second)
	for len(s.side.pending) > 0 && time.Now().Before(deadline) {
		s.flushSideSnapshotsMatching(false, nil)
		time.Sleep(20 * time.Millisecond)
	}
	saved, err := s.m.pg.SideSnapshot(context.Background(), key)
	if err != nil || saved == nil || saved.RunID != 2 || saved.Version != 1 {
		t.Fatalf("recovery: %+v %v", saved, err)
	}
	if len(s.side.retries) != 0 || len(s.side.latest) != 0 || len(s.side.pending) != 0 {
		t.Fatal("success retained dirty state")
	}
}

func TestCheckpointParentGoneClearsRetry(t *testing.T) {
	s := checkpointServer(t)
	snap := checkpointConversation(t, s)
	key := snap.Parent.Key()
	_, repair := checkpointFailure(t, s, key)
	s.publishSideSnapshot(snap)
	s.flushSideSnapshots()
	repair()
	if _, err := s.m.pg.Exec(`DELETE FROM conversations WHERE id=$1`, snap.Parent.ConversationID); err != nil {
		t.Fatal(err)
	}
	s.flushSideSnapshots()
	if len(s.side.retries) != 0 || len(s.side.latest) != 0 || len(s.side.pending) != 0 {
		t.Fatal("terminal parent retained dirty state")
	}
}

func TestCheckpointFailuresCapAndTerminalCompletionKeepsNewer(t *testing.T) {
	s := checkpointServer(t)
	snap := checkpointConversation(t, s)
	key := snap.Parent.Key()
	attempts, repair := checkpointFailure(t, s, key)
	s.publishSideSnapshot(snap)
	for i, want := range []time.Duration{1, 2, 4, 8, 16, 30, 30, 30} {
		started := time.Now()
		s.flushSideSnapshots()
		retry := s.side.retries[key]
		if attempts() != int64(i+1) || retry.failures > 6 || retry.nextAttempt.Before(started.Add(want*time.Second)) {
			t.Fatalf("attempt %d retry %+v", i, retry)
		}
		s.flushSideSnapshotsMatching(false, nil)
		if attempts() != int64(i+1) {
			t.Fatal("early retry")
		}
	}
	repair()
	tx, err := s.m.pg.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM conversations WHERE id=$1`, snap.Parent.ConversationID); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { s.flushSideSnapshots(); close(done) }()
	deadline := time.Now().Add(time.Second)
	for {
		s.side.mu.Lock()
		taken := len(s.side.pending) == 0
		s.side.mu.Unlock()
		if taken {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("flush did not start")
		}
		time.Sleep(time.Millisecond)
	}
	snap.RunID++
	s.publishSideSnapshot(snap)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	<-done
	if s.side.pending[key].RunID != 2 || s.side.latest[key].RunID != 2 || s.side.retries[key].failures != 6 {
		t.Fatal("terminal completion discarded newer checkpoint or retry")
	}
	s.flushSideSnapshots()
	if len(s.side.pending) != 0 || len(s.side.latest) != 0 || len(s.side.retries) != 0 {
		t.Fatal("newer terminal lookup retained state")
	}
}

func TestCheckpointNewerDuringSave(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			s := checkpointServer(t)
			snap := checkpointConversation(t, s)
			key := snap.Parent.Key()
			if fail {
				checkpointFailure(t, s, key)
			}
			tx, err := s.m.pg.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.Exec(`SELECT id FROM conversations WHERE id=$1 FOR UPDATE`, snap.Parent.ConversationID); err != nil {
				t.Fatal(err)
			}
			s.publishSideSnapshot(snap)
			done := make(chan struct{})
			go func() { s.flushSideSnapshots(); close(done) }()
			deadline := time.Now().Add(time.Second)
			for {
				s.side.mu.Lock()
				taken := len(s.side.pending) == 0
				s.side.mu.Unlock()
				if taken {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("flush did not start")
				}
				time.Sleep(time.Millisecond)
			}
			snap.Version++
			s.publishSideSnapshot(snap)
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			<-done
			if s.side.latest[key].Version != 2 || s.side.pending[key].Version != 2 {
				t.Fatal("old completion cleared newer checkpoint")
			}
			if fail {
				if s.side.retries[key].failures != 1 {
					t.Fatal("failure lost retry state")
				}
			} else {
				s.flushSideSnapshots()
				saved, err := s.m.pg.SideSnapshot(context.Background(), key)
				if err != nil || saved.Version != 2 {
					t.Fatalf("newer persistence: %+v %v", saved, err)
				}
			}
		})
	}
}

func TestCheckpointTimeoutRecoversWithoutPublication(t *testing.T) {
	s := checkpointServer(t)
	snap := checkpointConversation(t, s)
	key := snap.Parent.Key()
	tx, err := s.m.pg.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`SELECT id FROM conversations WHERE id=$1 FOR UPDATE`, snap.Parent.ConversationID); err != nil {
		t.Fatal(err)
	}
	s.publishSideSnapshot(snap)
	started := time.Now()
	s.flushSideSnapshots()
	if elapsed := time.Since(started); elapsed < 2800*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("timeout elapsed %v", elapsed)
	}
	if s.side.retries[key].failures != 1 || s.side.pending[key].Version != 1 {
		t.Fatal("timeout lost checkpoint")
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	s.flushSideSnapshots()
	if saved, err := s.m.pg.SideSnapshot(context.Background(), key); err != nil || saved == nil {
		t.Fatalf("timeout recovery %+v %v", saved, err)
	}
}

func TestCheckpointCancellationForcesDelayedAttempt(t *testing.T) {
	for _, repairBeforeCancel := range []bool{false, true} {
		t.Run(fmt.Sprint(repairBeforeCancel), func(t *testing.T) {
			s := checkpointServer(t)
			ctx, cancel := context.WithCancel(context.Background())
			s.ctx = ctx
			s.initSideQuestions()
			t.Cleanup(func() { cancel(); <-s.side.done })
			snap := checkpointConversation(t, s)
			attempts, repair := checkpointFailure(t, s, snap.Parent.Key())
			s.publishSideSnapshot(snap)
			s.flushSideSnapshots()
			if repairBeforeCancel {
				repair()
			}
			cancel()
			select {
			case <-s.side.done:
			case <-time.After(5 * time.Second):
				t.Fatal("final flush did not finish")
			}
			want := int64(2)
			if repairBeforeCancel {
				want = 1
			}
			if attempts() != want {
				t.Fatalf("attempts %d want %d", attempts(), want)
			}
			if repairBeforeCancel && len(s.side.pending) != 0 {
				t.Fatal("final success remained dirty")
			}
			if !repairBeforeCancel && len(s.side.pending) != 1 {
				t.Fatal("final failure discarded checkpoint")
			}
			time.Sleep(300 * time.Millisecond)
			if attempts() != want {
				t.Fatal("retry after worker exit")
			}
		})
	}
}

func TestCheckpointArchiveForcesRetryAndRecovers(t *testing.T) {
	s := checkpointServer(t)
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	s.m = m
	s.engine = NewEngine(m)
	task, err := m.CreateTask("checkpoint archive", "durability", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := m.pg.Exec(`DELETE FROM task_archives WHERE task_id=$1`, id); err != nil {
			t.Error(err)
		}
		if err := m.pg.DeleteTask(id); err != nil {
			t.Error(err)
		}
	})
	if err = m.pg.SetPaused(id, true); err != nil {
		t.Fatal(err)
	}
	snap := sidequestion.Snapshot{Parent: sidequestion.Parent{TaskID: id, ExplorationID: task.ExpID}, RunID: 1, Version: 1}
	attempts, repair := checkpointFailure(t, s, snap.Parent.Key())
	s.publishSideSnapshot(snap)
	s.flushSideSnapshots()
	unrelated := checkpointConversation(t, s)
	unrelatedAttempts, unrelatedRepair := checkpointFailure(t, s, unrelated.Parent.Key())
	s.publishSideSnapshot(unrelated)
	s.flushSideSnapshotsMatching(true, func(p sidequestion.Parent) bool { return p == unrelated.Parent })
	beforeUnrelated := unrelatedAttempts()
	job, err := m.pg.QueueTaskArchive(id)
	if err != nil {
		t.Fatal(err)
	}
	job, err = m.pg.ClaimTaskArchiveJob(context.Background())
	if err != nil || job == nil {
		t.Fatalf("claim archive: %+v %v", job, err)
	}
	if err = s.archiveTask(job); err == nil {
		t.Fatal("unsaved checkpoint permitted archive")
	}
	if attempts() != 2 || len(s.side.pending) != 2 {
		t.Fatal("archive did not force backed-off checkpoint")
	}
	if unrelatedAttempts() != beforeUnrelated {
		t.Fatal("task drain forced unrelated parent")
	}
	unrelatedRepair()
	s.flushSideSnapshotsMatching(true, func(p sidequestion.Parent) bool { return p == unrelated.Parent })
	var archived bool
	if err := m.pg.QueryRow(`SELECT archived_at IS NOT NULL FROM tasks WHERE id=$1`, id).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if archived {
		t.Fatal("failed archive committed")
	}
	repair()
	if err = s.archiveTask(job); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.QueryRow(`SELECT archived_at IS NOT NULL FROM tasks WHERE id=$1`, id).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if !archived {
		t.Fatal("archive recovery did not commit")
	}
	stored, err := m.pg.GetTaskArchive(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	extracted := t.TempDir()
	if err := extractTaskArchivePackage(stored.ArchivePath, stored.SHA256, extracted); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(extracted, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest db.TaskArchiveSnapshot
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Snapshot sidequestion.Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(manifest.Tables["side_question_sessions"], &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Snapshot.Parent != snap.Parent || rows[0].Snapshot.RunID != snap.RunID || rows[0].Snapshot.Version != snap.Version {
		t.Fatalf("archive checkpoint %+v", rows)
	}
	if len(s.side.pending) != 0 || len(s.side.retries) != 0 {
		t.Fatal("archive recovery retained dirty state")
	}
}
