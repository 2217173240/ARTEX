package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/artex/llmrec"
)

func TestTriggerAttributionTasklessContext(t *testing.T) {
	s := &Server{}
	for _, taskID := range []int64{0, -1} {
		base := intercept.WithConvID(t.Context(), 123)
		base = intercept.WithReviewContext(base, "/conversation", intercept.ReviewBackground{Source: intercept.BackgroundUserMessage, Text: "human input"})
		base, cancel := context.WithCancelCause(base)
		ctx := s.triggerAttributionContext(base, "reporter", taskID)
		if intercept.ConvIDFromContext(ctx) != 123 || intercept.TaskIDFromContext(ctx) != "" || llmrec.TaskIDFrom(ctx) != "" || llmrec.ExplorationIDFrom(ctx) != 0 {
			t.Fatal("taskless metering changed conversation or task authority")
		}
		input, err := intercept.BuildReviewInput(ctx, "Read", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if input.Background == nil || input.Background.Text != "human input" || input.WorkingDir != "/conversation" {
			t.Fatalf("review input=%+v", input)
		}
		cause := errors.New("manual stop")
		cancel(cause)
		if context.Cause(ctx) != cause {
			t.Fatal("cancellation cause lost")
		}
	}
	ctx := s.triggerAttributionContext(intercept.WithConvID(t.Context(), 123), "reporter", 0)
	input, err := intercept.BuildReviewInput(ctx, "Read", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if input.Background != nil {
		t.Fatal("trigger attribution supplied human background")
	}
}

func TestTriggerAttributionRealTaskLookup(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	task, err := pg.CreateTask("trigger attribution", "verify ownership", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := pg.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID); err != nil {
			t.Error(err)
		}
		if _, err := pg.Exec(`DELETE FROM explorations WHERE id=$1`, task.ExplorationID); err != nil {
			t.Error(err)
		}
	}()
	s := &Server{m: &Manager{pg: pg}}
	base := intercept.WithConvID(t.Context(), 123)
	ctx := s.triggerAttributionContext(base, "reporter", task.ID)
	if llmrec.TaskIDFrom(ctx) != strconv.FormatInt(task.ID, 10) || llmrec.ExplorationIDFrom(ctx) != task.ExplorationID {
		t.Fatal("task/exploration lookup lost ownership")
	}
	if intercept.TaskIDFromContext(ctx) != "" || intercept.ConvIDFromContext(ctx) != 123 {
		t.Fatal("metering changed tool authorization")
	}
	missing := s.triggerAttributionContext(base, "reporter", 9223372036854775807)
	if llmrec.TaskIDFrom(missing) != "9223372036854775807" || llmrec.ExplorationIDFrom(missing) != 0 {
		t.Fatal("missing optional exploration lost known task")
	}
	canceled, cancel := context.WithCancel(base)
	cancel()
	ctx = s.triggerAttributionContext(canceled, "reporter", task.ID)
	if ctx.Err() != context.Canceled || llmrec.TaskIDFrom(ctx) != strconv.FormatInt(task.ID, 10) || llmrec.ExplorationIDFrom(ctx) != 0 {
		t.Fatal("failed optional lookup changed cancellation/task")
	}
}
