package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

func TestCaptureStreamInterruptedReceiptsKeepTruthfulOutcomes(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{{"success", "已正常返回"}, {"error", "已返回错误"}, {"interrupted", "执行结果未知"}, {"queued", "在队列中被中断，未执行"}} {
		t.Run(tc.mode, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			started, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			probe := tool.Build(tool.Spec{Name: "StreamProbe", Schema: map[string]any{"type": "object"}, Run: func(ctx context.Context, _ json.RawMessage, tcx *tool.ToolContext) (tool.Result, error) {
				calls.Add(1)
				if tc.mode == "success" {
					return tool.Text("actual success"), nil
				}
				if tc.mode == "error" {
					return tool.Errorf("actual error"), nil
				}
				if tcx.ToolUseID != "running" {
					t.Errorf("unexpected invocation: %s", tcx.ToolUseID)
					return tool.Text("unexpected"), nil
				}
				close(started)
				<-ctx.Done()
				<-release
				defer close(stopped)
				return tool.Text("late success"), nil
			}})
			turns := 0
			provider := captureUsageProvider{stream: func(_ context.Context, y func(llm.StreamEvent, error) bool) {
				turns++
				if turns == 2 {
					cancel(AbortPausedByUser)
					y(llm.StreamEvent{}, ctx.Err())
					return
				}
				events := []llm.StreamEvent{{Type: llm.SEToolUseStart, ToolID: "running", ToolName: "StreamProbe"}, {Type: llm.SEToolInputJSON, Text: `{}`}}
				if tc.mode == "success" || tc.mode == "error" {
					events = append(events, llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "tool_use"})
					for _, e := range events {
						if !y(e, nil) {
							return
						}
					}
					return
				}
				if tc.mode == "queued" {
					events = append(events, llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: "queued", ToolName: "StreamProbe"}, llm.StreamEvent{Type: llm.SEToolInputJSON, Text: `{}`})
				}
				events = append(events, llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: "pending", ToolName: "StreamProbe"}, llm.StreamEvent{Type: llm.SEToolInputJSON, Text: `{"partial`})
				for _, e := range events {
					if !y(e, nil) {
						return
					}
				}
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Error("tool did not start")
				}
				cancel(AbortPausedByUser)
				y(llm.StreamEvent{}, ctx.Err())
			}}
			var activities []db.Activity
			_, reason, err := captureRun(ctx, agentcore.Options{Provider: provider, Tools: []tool.CoreTool{probe}, PermissionMode: permission.ModeBypass, WorkingDir: t.TempDir()}, "synthetic stream interruption", func(a db.Activity) { activities = append(activities, a) })
			if tc.mode == "interrupted" || tc.mode == "queued" {
				close(release)
				select {
				case <-stopped:
				case <-time.After(time.Second):
					t.Fatal("tool child context leaked")
				}
			}
			if reason != harness.ReasonAbortedStreaming || err == nil || calls.Load() != 1 {
				t.Fatalf("reason=%s err=%v calls=%d", reason, err, calls.Load())
			}
			result := captureOutputResult(t, activities)
			if !strings.Contains(result.Detail, tc.want) || !strings.Contains(result.Detail, "paused_by_user") {
				t.Fatalf("receipt or cancellation cause lost: %s", result.Detail)
			}
			if tc.mode != "success" && strings.Contains(result.Detail, "已正常返回") {
				t.Fatalf("receipt claimed successful tool completion: %s", result.Detail)
			}
			for _, a := range activities {
				if a.Kind == "tool_use" && a.ToolUseID == "pending" {
					t.Fatal("unsubmitted input emitted as real tool_use")
				}
			}
		})
	}
}
