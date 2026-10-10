package main

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
)

func TestLaunchParentEOFStopsBackend(t *testing.T) {
	ctx, stop := context.WithCancelCause(context.Background())
	defer stop(nil)
	r, w := io.Pipe()
	defer r.Close()
	go watchLaunchParent(ctx, stop, r)
	select {
	case <-ctx.Done():
		t.Fatal("stopped while supervisor pipe alive")
	default:
	}
	_ = w.Close()
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), agent.AbortShutdown) {
			t.Fatal(context.Cause(ctx))
		}
	case <-time.After(time.Second):
		t.Fatal("orphan backend was not stopped")
	}
}
