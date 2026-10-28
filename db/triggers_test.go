package db

import "testing"

func TestNewToolCallsSincePairsResults(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	task, err := d.CreateTask("工具触发配对", "保留结果及其准确入参", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID); err != nil {
			t.Error(err)
		}
		if _, err := d.Exec(`DELETE FROM explorations WHERE id=$1`, task.ExplorationID); err != nil {
			t.Error(err)
		}
	})
	es := d.Exploration(task.ExplorationID)
	other, err := d.CreateTask("其他任务", "隔离入参", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Exec(`DELETE FROM tasks WHERE id=$1`, other.ID); err != nil {
			t.Error(err)
		}
		if _, err := d.Exec(`DELETE FROM explorations WHERE id=$1`, other.ExplorationID); err != nil {
			t.Error(err)
		}
	})
	if _, err := d.Exploration(other.ExplorationID).AppendActivity(Activity{Kind: "tool_use", ToolUseID: "missing", Worker: "a", Tool: "Bash", Detail: "other-task-input"}); err != nil {
		t.Fatal(err)
	}
	node, err := es.AddIntent(map[string]any{"summary": "配对意图"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	zero, one := 0, 1
	type expected struct {
		id            int64
		input, output string
		isErr         bool
	}
	var want []expected
	appendRow := func(a Activity) int64 {
		t.Helper()
		id, err := es.AppendActivity(a)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	use := func(call, worker, tool, input string, n *int64, seg *int) {
		appendRow(Activity{Kind: "tool_use", ToolUseID: call, Worker: worker, Tool: tool, Detail: input, NodeID: n, MainSeg: seg})
	}
	result := func(call, worker, tool, input, output string, n *int64, seg *int, isErr bool) {
		id := appendRow(Activity{Kind: "tool_result", ToolUseID: call, Worker: worker, Tool: tool, Detail: output, NodeID: n, MainSeg: seg, IsError: isErr})
		want = append(want, expected{id, input, output, isErr})
	}
	use("shared", "a", "Bash", "input-a", nil, nil)
	use("shared", "b", "Bash", "input-b", nil, nil)
	result("shared", "a", "Bash", "input-a", "output-a", nil, nil, false)
	result("shared", "b", "Bash", "input-b", "output-b", nil, nil, true)
	use("shared", "a", "Bash", "next-a", nil, nil)
	result("shared", "a", "Bash", "next-a", "next-output", nil, nil, false)
	use("intent", "a", "Bash", "intent-input", &node, nil)
	use("intent", "a", "Bash", "null-input", nil, nil)
	result("intent", "a", "Bash", "intent-input", "intent-output", &node, nil, false)
	result("intent", "a", "Bash", "null-input", "null-output", nil, nil, false)
	use("segment", "mainagent", "Bash", "legacy-input", nil, nil)
	use("segment", "mainagent", "Bash", "new-input", nil, &one)
	result("segment", "mainagent", "Bash", "legacy-input", "zero-output", nil, &zero, false)
	result("segment", "mainagent", "Bash", "new-input", "one-output", nil, &one, false)
	use("tool", "a", "Read", "wrong-tool", nil, nil)
	result("tool", "a", "Bash", "", "tool-output", nil, nil, false)
	use("", "", "Bash", "unknown-input", nil, nil)
	result("", "", "Bash", "", "unknown-output", nil, nil, false)
	// 历史空字符串 ID 同样表示缺失，不能作为关联标识。
	legacyUse := appendRow(Activity{Kind: "tool_use", Tool: "Bash", Detail: "legacy-unknown"})
	legacyResult := appendRow(Activity{Kind: "tool_result", Tool: "Bash", Detail: "legacy-output"})
	if _, err := d.Exec(`UPDATE activity SET tool_use_id='' WHERE id IN ($1,$2)`, legacyUse, legacyResult); err != nil {
		t.Fatal(err)
	}
	want = append(want, expected{legacyResult, "", "legacy-output", false})
	result("future", "a", "Bash", "", "future-output", nil, nil, true)
	use("future", "a", "Bash", "future-input", nil, nil)
	result("missing", "a", "Bash", "", "unpaired-output", nil, nil, false)
	use("null-worker", "", "Bash", "null-worker-input", nil, nil)
	result("null-worker", "", "Bash", "null-worker-input", "null-worker-output", nil, nil, false)
	// 游标只限制结果；较早的入参仍然参与新结果的配对。
	cursor := want[len(want)-1].id
	result("shared", "a", "Bash", "next-a", "after-cursor", nil, nil, false)
	events, err := d.NewToolCallsSince(0)
	if err != nil {
		t.Fatal(err)
	}
	var got []TaskEvent
	for _, e := range events {
		if e.TaskID == task.ID {
			got = append(got, e)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		e := got[i]
		if e.NodeID != w.id || e.ToolInput != w.input || e.ToolOutput != w.output || e.ToolIsErr != w.isErr || e.TaskDesc != task.Description || e.TaskGoal != task.Goal || e.Tool != "Bash" {
			t.Errorf("event %d = %+v, want %+v", i, e, w)
		}
		var detail string
		if err := d.QueryRow(`SELECT detail FROM activity WHERE id=$1`, w.id).Scan(&detail); err != nil || detail != w.output {
			t.Fatalf("stored output changed: %q %v", detail, err)
		}
	}
	events, err = d.NewToolCallsSince(cursor)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, e := range events {
		if e.TaskID == task.ID {
			got = append(got, e)
		}
	}
	if len(got) != 1 || got[0].NodeID != want[len(want)-1].id || got[0].ToolInput != "next-a" {
		t.Fatalf("cursor events: %+v", got)
	}
	if _, err := d.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	events, err = d.NewToolCallsSince(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.TaskID == task.ID {
			t.Fatalf("deleted task event: %+v", e)
		}
	}
}
