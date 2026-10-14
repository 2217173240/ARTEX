package db

import (
	"fmt"
	"os"
	"testing"
)

func TestTaskMetricsScopedMatchesBatch(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("requires isolated ARTEX_PG_DSN fixture")
	}
	d, err := Open(os.Getenv("ARTEX_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	task, err := d.CreateTask("metric fixture", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	if _, err := d.Exec(`INSERT INTO activity(exploration_id,kind,input_tokens,output_tokens) VALUES ($1,'result',13,5),($1,'tool_use',100,100)`, task.ExplorationID); err != nil {
		t.Fatal(err)
	}
	all, err := d.TaskListMetricsAll()
	if err != nil {
		t.Fatal(err)
	}
	one, err := d.TaskListMetricsForTask(fmt.Sprint(task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if one != all[task.ExplorationID] || one.Tokens.InputTokens != 13 || one.Tokens.OutputTokens != 5 || one.LastActivity == 0 {
		t.Fatalf("scoped %+v batch %+v", one, all[task.ExplorationID])
	}
	empty, err := d.TaskListMetricsForTask("")
	if err == nil || err.Error() != "task ID is required" || empty != (TaskListMetrics{}) {
		t.Fatalf("empty task ID metrics %+v: %v", empty, err)
	}
	// A missing ID is rejected before querying any task, even without a DB.
	if _, err := (*DB)(nil).TaskListMetricsForTask(""); err == nil {
		t.Fatal("empty task ID did not reject before querying")
	}
	if _, err := d.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	one, err = d.TaskListMetricsForTask(fmt.Sprint(task.ID))
	if err != nil || one != (TaskListMetrics{}) {
		t.Fatalf("deleted metrics %+v: %v", one, err)
	}
}
