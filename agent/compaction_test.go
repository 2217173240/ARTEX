package agent

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

type controlledCompactionProvider struct {
	started chan context.Context
	result  chan error
}

func (p *controlledCompactionProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("compaction must use Complete"))
	}
}

func (p *controlledCompactionProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.started <- ctx
	// Return a successful body even after ctx is cancelled. The compactor must
	// enforce cancellation before writing, independently of provider behavior.
	err := <-p.result
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("folded facts")}}, "end_turn", llm.Usage{}, err
}

func newCompactionFixture(t *testing.T) (*Compactor, *db.ExplorationStore, *controlledCompactionProvider) {
	t.Helper()
	d := testDB(t)
	t.Cleanup(func() { _ = d.Close() })
	expID, err := d.CreateExploration("compaction fixture", "controlled fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID) })
	ts := d.Exploration(expID)
	first, err := ts.AddNode(db.KindFact, map[string]any{"summary": "first"}, 0, "confirmed", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ts.AddNode(db.KindFact, map[string]any{"summary": "second"}, 0, "confirmed", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Link(first, db.RelDerivedFrom, second); err != nil {
		t.Fatal(err)
	}
	p := &controlledCompactionProvider{started: make(chan context.Context, 2), result: make(chan error, 2)}
	t.Cleanup(func() { p.result <- errors.New("test cleanup") })
	c := NewCompactor(p, "fake")
	c.n = 2
	c.params.R = 0
	return c, ts, p
}

func waitCompactionSignal[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for compaction")
		var zero T
		return zero
	}
}

func TestCompactorAdmissionCoversDigestCommit(t *testing.T) {
	c, ts, p := newCompactionFixture(t)
	admitted := false
	released := make(chan error, 1)
	c.SetTaskAdmission(func() (func(), bool) {
		admitted = true
		return func() {
			digests, err := ts.ActiveDigests()
			if err == nil && len(digests) != 1 {
				err = errors.New("admission released before digest was committed")
			}
			if err == nil {
				members, memberErr := ts.DigestMembers(digests[0].ID)
				err = memberErr
				if err == nil && len(members) != 2 {
					err = errors.New("digest did not cover both facts")
				}
			}
			released <- err
		}, true
	})
	c.OnPlannerRound(context.Background(), ts)
	if !admitted {
		t.Fatal("OnPlannerRound returned before task admission")
	}
	waitCompactionSignal(t, p.started)
	select {
	case <-released:
		t.Fatal("task admission released during Complete")
	default:
	}
	p.result <- nil
	if err := waitCompactionSignal(t, released); err != nil {
		t.Fatal(err)
	}
	// A normal successful fold still observes the existing cooldown.
	first, err := ts.AddNode(db.KindFact, map[string]any{"summary": "next first"}, 0, "confirmed", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ts.AddNode(db.KindFact, map[string]any{"summary": "next second"}, 0, "confirmed", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Link(first, db.RelDerivedFrom, second); err != nil {
		t.Fatal(err)
	}
	c.OnPlannerRound(context.Background(), ts)
	select {
	case <-p.started:
		t.Fatal("successful fold ignored cooldown")
	case <-time.After(40 * time.Millisecond):
	}
}

func TestCompactorCancelledSuccessDoesNotWriteDigest(t *testing.T) {
	c, ts, p := newCompactionFixture(t)
	released := make(chan struct{}, 1)
	c.SetTaskAdmission(func() (func(), bool) {
		return func() { released <- struct{}{} }, true
	})
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	c.OnPlannerRound(ctx, ts)
	providerCtx := waitCompactionSignal(t, p.started)
	wantCause := errors.New("task stopped")
	cancel(wantCause)
	if !errors.Is(context.Cause(providerCtx), wantCause) {
		t.Fatalf("provider cancellation cause=%v", context.Cause(providerCtx))
	}
	p.result <- nil
	waitCompactionSignal(t, released)
	digests, err := ts.ActiveDigests()
	if err != nil || len(digests) != 0 {
		t.Fatalf("cancelled compaction persisted digest: digests=%v err=%v", digests, err)
	}
}

func TestCompactorRejectedAdmissionCanRetry(t *testing.T) {
	c, ts, p := newCompactionFixture(t)
	accept := false
	released := make(chan struct{}, 1)
	c.SetTaskAdmission(func() (func(), bool) {
		if !accept {
			return nil, false
		}
		return func() { released <- struct{}{} }, true
	})
	c.OnPlannerRound(context.Background(), ts)
	select {
	case <-p.started:
		t.Fatal("rejected admission started Complete")
	default:
	}
	accept = true
	c.OnPlannerRound(context.Background(), ts)
	waitCompactionSignal(t, p.started)
	p.result <- errors.New("controlled failure")
	waitCompactionSignal(t, released)
	c.mu.Lock()
	running := c.running[ts.ID()]
	c.mu.Unlock()
	if running {
		t.Fatal("failed Complete did not release per-instance running state")
	}
}

func TestCompactorCancelledRoundDoesNotMaintain(t *testing.T) {
	c, ts, _ := newCompactionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.OnPlannerRound(ctx, ts)
	round, err := ts.RoundNo()
	if err != nil || round != 0 {
		t.Fatalf("cancelled planner round changed bookkeeping: round=%d err=%v", round, err)
	}
}

func TestMajorCompactionPreservesOldDigestUntilReplacement(t *testing.T) {
	for _, outcome := range []string{"cancel", "provider_error", "success"} {
		t.Run(outcome, func(t *testing.T) {
			c, ts, p := newCompactionFixture(t)
			nodes, err := ts.Nodes(10)
			if err != nil {
				t.Fatal(err)
			}
			members := []int64{nodes[0].ID, nodes[1].ID}
			old, err := ts.AddDigest(map[string]any{"body": "old summary", "signature": "stale", "member_ids": members, "generation": 1}, members)
			if err != nil {
				t.Fatal(err)
			}
			c.m = 1
			released := make(chan struct{}, 1)
			c.SetTaskAdmission(func() (func(), bool) { return func() { released <- struct{}{} }, true })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.OnPlannerRound(ctx, ts)
			waitCompactionSignal(t, p.started)
			assertOld := func() {
				t.Helper()
				active, err := ts.ActiveDigests()
				if err != nil || len(active) != 1 || active[0].ID != old {
					t.Fatalf("old digest unavailable: active=%v err=%v", active, err)
				}
				shown, _ := coldDigestsRecent(ts, 10)
				if len(shown) != 1 || shown[0]["body"] != "old summary" {
					t.Fatalf("old context unavailable: %v", shown)
				}
				covered, err := ts.CoveredMembers()
				if err != nil || len(covered) != 2 {
					t.Fatalf("old covers unavailable: %v %v", covered, err)
				}
			}
			assertOld()
			var providerErr error
			if outcome == "cancel" {
				cancel()
			}
			if outcome == "provider_error" {
				providerErr = errors.New("controlled failure")
			}
			p.result <- providerErr
			waitCompactionSignal(t, released)
			if outcome != "success" {
				assertOld()
			} else {
				active, err := ts.ActiveDigests()
				if err != nil || len(active) != 1 || active[0].ID == old {
					t.Fatalf("replacement not published: %v %v", active, err)
				}
			}
			for _, id := range members {
				if n, err := ts.GetNode(id); err != nil || n == nil {
					t.Fatalf("raw member lost: %d %v", id, err)
				}
			}
		})
	}
}

func TestDigestReplacementWriteFailureKeepsOldCoverage(t *testing.T) {
	_, ts, _ := newCompactionFixture(t)
	nodes, err := ts.Nodes(10)
	if err != nil {
		t.Fatal(err)
	}
	members := []int64{nodes[0].ID, nodes[1].ID}
	old, err := ts.AddDigest(map[string]any{"body": "old summary"}, members)
	if err != nil {
		t.Fatal(err)
	}
	// The first replacement is inserted before the second one's invalid member
	// fails the foreign key. Neither partial publication nor retirement may leak.
	err = ts.ReplaceDigests(context.Background(), []int64{old}, []db.DigestReplacement{
		{Payload: map[string]any{"body": "replacement"}, MemberIDs: members},
		{Payload: map[string]any{"body": "invalid"}, MemberIDs: []int64{-1}},
	})
	if err == nil {
		t.Fatal("invalid member did not fail replacement write")
	}
	active, err := ts.ActiveDigests()
	if err != nil || len(active) != 1 || active[0].ID != old {
		t.Fatalf("failed transaction changed active digests: %v %v", active, err)
	}
	covered, err := ts.CoveredMembers()
	if err != nil || len(covered) != 2 {
		t.Fatalf("failed transaction changed coverage: %v %v", covered, err)
	}
	for _, member := range members {
		if covered[member] != old {
			t.Fatalf("member %d lost old coverage: %v", member, covered)
		}
	}
}
