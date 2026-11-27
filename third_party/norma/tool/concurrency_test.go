package tool

import (
	"testing"
)

func TestConcurrentReadOnlyTools(t *testing.T) {
	wf := NewWebFetch(WebFetchConfig{})
	if !wf.IsConcurrencySafe(nil) {
		t.Fatal("WebFetch.IsConcurrencySafe(nil) = false, want true")
	}

	reg := NewRegistry()
	search := NewSearchExtraTools(reg, []string{"mcp__demo__scan"})
	if !search.IsConcurrencySafe(nil) {
		t.Fatal("SearchExtraTools.IsConcurrencySafe(nil) = false, want true")
	}
	if !search.IsReadOnly(nil) {
		t.Fatal("SearchExtraTools.IsReadOnly(nil) = false, want true")
	}

	exec := NewExecuteExtraTool(reg, nil)
	if exec.IsConcurrencySafe(nil) {
		t.Fatal("ExecuteExtraTool.IsConcurrencySafe(nil) = true, want false")
	}
}
