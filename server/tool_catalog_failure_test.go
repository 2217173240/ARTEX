package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// Managers created without Server initialization still need their own live
// catalog callbacks; retaining another fixture's callbacks retains its DB too.
func wireFixtureToolAssembly(t *testing.T, pg *db.DB) {
	t.Helper()
	oldAugment, oldResolve, oldPolicy := agent.ToolAugment, agent.ToolResolve, agent.FindingTrafficBindingEnabled
	t.Cleanup(func() {
		agent.ToolAugment, agent.ToolResolve, agent.FindingTrafficBindingEnabled = oldAugment, oldResolve, oldPolicy
	})
	wireAgentAugment(pg, t.TempDir(), nil)
	wireTools(pg, nil)
}

// A separate live connection keeps task storage available while the actual
// catalog connection fails. All four production runners have nil providers:
// reaching model execution would panic instead of satisfying this assertion.
func TestToolCatalogFailureStopsRunners(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close() })
	catalog, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close() })
	oldAugment, oldResolve, oldPolicy := agent.ToolAugment, agent.ToolResolve, agent.FindingTrafficBindingEnabled
	t.Cleanup(func() {
		agent.ToolAugment, agent.ToolResolve, agent.FindingTrafficBindingEnabled = oldAugment, oldResolve, oldPolicy
	})
	// The supported standalone augmenter configuration isolates optional MCP and
	// skill services; catalog resolution still uses the production database hook.
	agent.ToolAugment = nil
	wireTools(catalog, nil)
	for _, key := range []string{"goal_met", "record_fact"} {
		row, err := pg.GetTool(key)
		if err != nil || row == nil {
			t.Fatalf("catalog control %s missing: %v", key, err)
		}
		t.Cleanup(func() {
			bindings, _ := json.Marshal(row.Agents)
			if err := pg.UpdateTool(row.Key, row.Description, row.Schema, bindings, row.Enabled); err != nil {
				t.Error(err)
			}
		})
		bindings := json.RawMessage(`[]`)
		enabled := true
		if key == "record_fact" {
			bindings, enabled = json.RawMessage(`["worker"]`), false
		}
		if err := pg.UpdateTool(key, row.Description, row.Schema, bindings, enabled); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	ts := agent.NewToolSet(nil, "catalog-control")
	for _, control := range []struct {
		role   string
		base   []actool.CoreTool
		denied string
	}{
		{"planner", ts.PlannerTools(), "goal_met"},
		{"worker", ts.WorkerTools(), "record_fact"},
	} {
		if names(control.base)[control.denied] == nil {
			t.Fatalf("control %s must exist in the incoming base", control.denied)
		}
		tools, _, cleanup, err := agent.AugmentTools(ctx, control.role, control.base)
		cleanup()
		if err != nil {
			t.Fatal(err)
		}
		if names(tools)[control.denied] != nil {
			t.Fatalf("healthy catalog exposed %s", control.denied)
		}
	}
	task, err := pg.CreateTask("catalog failure fixture", "do not execute tools", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pg.DeleteTaskCascade(task.ID, true, true); err != nil {
			t.Error(err)
		}
	})
	store := pg.Exploration(task.ExplorationID)
	iid, err := store.AddIntent(map[string]any{"summary": "catalog failure fixture"}, 1, nil, "human")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := store.GetNode(iid)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	_, failure := catalog.ListTools()
	if failure == nil {
		t.Fatal("closed catalog database did not fail")
	}
	assertCatalogError := func(t *testing.T, err error) {
		t.Helper()
		var catalogErr *toolCatalogError
		if !errors.As(err, &catalogErr) || !errors.Is(err, failure) {
			t.Fatalf("expected preserved catalog failure, got %v", err)
		}
		if err.Error() == catalogErr.Unwrap().Error() {
			t.Fatal("public error exposed raw database failure")
		}
	}
	tools, def, cleanup, err := agent.AugmentTools(ctx, "planner", ts.PlannerTools())
	cleanup()
	assertCatalogError(t, err)
	if tools == nil || len(tools) != 0 || def.Unlock != nil || def.UnlockSkill != nil || len(def.Deferred) != 0 {
		t.Fatal("catalog failure exposed tools or deferred capabilities")
	}
	dir := t.TempDir()
	t.Run("mainagent", func(t *testing.T) {
		_, err := agent.NewMainAgent(nil, "", dir, nil, 0, 0).Chat(ctx, task.ID, 0, nil, store, "", "", nil, nil, nil, nil, nil)
		assertCatalogError(t, err)
	})
	t.Run("planner", func(t *testing.T) {
		_, _, err := agent.NewPlanner(nil, "", dir, nil, 0, 0).Plan(ctx, task.ID, nil, store, "", nil, nil)
		assertCatalogError(t, err)
	})
	t.Run("worker", func(t *testing.T) {
		_, _, err := agent.NewWorker(nil, "", dir, nil, 0, 0).Execute(ctx, "fixture", task.ID, nil, store, intent, nil, nil, nil, nil)
		assertCatalogError(t, err)
	})
	t.Run("chat", func(t *testing.T) {
		_, err := agent.NewChatAgent(nil, "", dir, nil, 0).Chat(ctx, "custom-fixture", "catalog-fixture", "", 0, 0, false, nil)
		assertCatalogError(t, err)
	})
}
