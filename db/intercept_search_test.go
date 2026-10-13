package db

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestInterceptHistorySearch(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	marker := "history-search-" + t.Name()
	task, err := d.CreateTaskWithOptions(marker+" description", marker+" goal", TaskCreateOptions{Name: marker + " task-name"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID)
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, task.ExplorationID)
	})
	conv, err := d.CreateConversation(marker+" conv-agent", marker+" title", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM conversations WHERE id=$1`, conv.ID) })
	rule, err := d.CreateInterceptRule(marker+" rule-name", "tool_name", "string", "Bash", "ask", "fixture", 1, true, false, 0, "deny")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID) })
	create := func(taskID, reason string, stamp time.Time) int64 {
		t.Helper()
		id, err := d.CreateDecidedIntercept(rule.ID, conv.ID, taskID, marker+" agent", marker+" tool", []byte(`{"command":"`+marker+` input"}`), "denied", reason, &InterceptAudit{UserMessage: marker + " audit"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_pending WHERE id=$1`, id) })
		if _, err := d.Exec(`UPDATE intercept_pending SET created_at=$2 WHERE id=$1`, id, stamp); err != nil {
			t.Fatal(err)
		}
		return id
	}
	taskID := fmt.Sprint(task.ID)
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	before := create(taskID, marker+` literal x _ \`, start.Add(-time.Nanosecond*1000))
	first := create(taskID, marker+` literal % _ \`, start)
	last := create(taskID, marker+` literal % x \`, end.Add(-time.Nanosecond*1000))
	after := create(taskID, marker+" literal % _ x", end)
	other := create("nonnumeric-"+marker, marker+" other", start)
	check := func(scope string, filter InterceptApprovalFilter, want []int64) {
		t.Helper()
		var rows []InterceptApprovalRow
		var total int
		var err error
		if scope == "" {
			rows, total, err = d.ListAllInterceptsPage(1, 100, filter)
		} else {
			rows, total, err = d.ListTaskInterceptsPage(scope, 1, 100, filter)
		}
		ids := make([]int64, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		if err != nil || total != len(want) || !slices.Equal(ids, want) {
			t.Fatalf("search %q scope %q: count=%d ids=%v want=%v err=%v", filter.Search, scope, total, ids, want, err)
		}
	}
	allTask := []int64{after, last, first, before}
	for _, text := range []string{"title", "conv-agent", "task-name", "description", "goal", "rule-name", "agent", "tool", "input", "audit"} {
		check(taskID, InterceptApprovalFilter{Search: marker + " " + text}, allTask)
	}
	check(taskID, InterceptApprovalFilter{Search: "literal %"}, []int64{after, last, first})
	check(taskID, InterceptApprovalFilter{Search: "literal % _"}, []int64{after, first})
	check(taskID, InterceptApprovalFilter{Search: `literal % _ \`}, []int64{first})
	check(taskID, InterceptApprovalFilter{Search: fmt.Sprintf(" #%d ", first)}, []int64{first})
	check(taskID, InterceptApprovalFilter{Search: fmt.Sprintf("#%d", other)}, nil)
	filter := InterceptApprovalFilter{Search: marker, CreatedFrom: &start, CreatedTo: &end, Status: "denied", DecisionSource: "rule"}
	check(taskID, filter, []int64{last, first})
	check("", filter, []int64{last, other, first})
	for page, want := range [][]int64{{last}, {first}, {}} {
		rows, total, err := d.ListTaskInterceptsPage(taskID, page+1, 1, filter)
		var ids []int64
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		if err != nil || total != 2 || !slices.Equal(ids, want) {
			t.Fatalf("page=%d count=%d ids=%v want=%v err=%v", page+1, total, ids, want, err)
		}
	}
	// Calendar-day bounds are instants even if PostgreSQL uses another timezone.
	d.SetMaxOpenConns(1)
	if _, err := d.Exec(`SET TIME ZONE 'Pacific/Honolulu'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`SET TIME ZONE 'UTC'`) })
	check(taskID, filter, []int64{last, first})
	if _, err := d.Exec(`UPDATE tasks SET archived_at=NOW(), deleted_at=NOW() WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	check(taskID, InterceptApprovalFilter{Search: marker + " task-name"}, allTask)
	if _, err := d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	// Hard-deleted task rows still leave approval history readable; string task
	// identifiers never need to cast to bigint to join.
	check(taskID, filter, []int64{last, first})
	check("nonnumeric-"+marker, filter, []int64{other})
	for _, search := range []string{"#0", "#abc"} {
		if _, _, err := d.ListTaskInterceptsPage(taskID, 1, 20, InterceptApprovalFilter{Search: search}); err == nil {
			t.Fatalf("invalid direct search accepted: %s", search)
		}
	}
}
