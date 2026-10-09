package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/intercept"
	"testing"
)

func TestFindingRecordChatToolsUseStandaloneSourceOwnedID(t *testing.T) {
	s, task, ids := testCaseServer(t)
	s.m.tasks = map[string]*Task{i64s(task): {ID: i64s(task)}}
	tools := s.findingMutationTools()
	in := json.RawMessage(fmt.Sprintf(`{"task_id":%d,"finding_id":%d,"severity":"low","status":"fixed"}`, task, ids[0]))
	result, err := tools[0].Call(context.Background(), in, nil)
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
	f, _ := s.m.pg.GetFinding(ids[0])
	if f.Severity != "low" || f.Status != "fixed" {
		t.Fatalf("chat edit not saved %+v", f)
	}
	wrong := json.RawMessage(fmt.Sprintf(`{"task_id":%d,"finding_id":%d}`, task+100000, ids[0]))
	result, err = tools[1].Call(context.Background(), wrong, nil)
	if err != nil || !result.IsError {
		t.Fatal("unknown task allowed", result, err)
	}
	f, _ = s.m.pg.GetFinding(ids[0])
	if f == nil {
		t.Fatal("unauthorized delete changed record")
	}
	result, err = tools[1].Call(context.Background(), in, nil)
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
	f, _ = s.m.pg.GetFinding(ids[0])
	if f != nil {
		t.Fatal("chat delete left record")
	}
}

func TestFindingRecordChatToolsRespectRuntimeTaskContext(t *testing.T) {
	s, source, ids := testCaseServer(t)
	child, err := s.m.pg.CreateTask("inherited mutation fixture", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.m.pg.DeleteTask(child.ID) })
	s.m.tasks = map[string]*Task{i64s(source): {ID: i64s(source)}, i64s(child.ID): {ID: i64s(child.ID), SourceTaskIDs: []int64{source}}}
	tools := s.findingMutationTools()
	own := json.RawMessage(fmt.Sprintf(`{"task_id":%d,"finding_id":%d,"severity":"low"}`, source, ids[0]))
	childInput := json.RawMessage(fmt.Sprintf(`{"task_id":%d,"finding_id":%d,"severity":"low"}`, child.ID, ids[0]))
	childCtx := intercept.WithTaskContext(context.Background(), i64s(child.ID), "chat", nil)
	for _, tool := range tools {
		result, err := tool.Call(childCtx, own, nil)
		if err != nil || !result.IsError {
			t.Fatal("context mismatch allowed", tool.Name(), result, err)
		}
		result, err = tool.Call(childCtx, childInput, nil)
		if err != nil || !result.IsError {
			t.Fatal("inherited record mutation allowed", tool.Name(), result, err)
		}
	}
	f, _ := s.m.pg.GetFinding(ids[0])
	if f == nil || f.Severity != "high" {
		t.Fatal("rejected mutation changed source record")
	}
	ownCtx := intercept.WithTaskContext(context.Background(), i64s(source), "chat", nil)
	result, err := tools[0].Call(ownCtx, own, nil)
	if err != nil || result.IsError {
		t.Fatal("source task mutation rejected", result, err)
	}
	f, _ = s.m.pg.GetFinding(ids[0])
	if f.Severity != "low" {
		t.Fatal("source task mutation missing")
	}
}

func TestHistoricalFindingReviewRejectsSelectedRecordMutations(t *testing.T) {
	s, task, ids := testCaseServer(t)
	s.m.tasks = map[string]*Task{i64s(task): {ID: i64s(task)}}
	runs, err := s.m.pg.CreateFindingCaseReviews(t.Context(), map[int64][]int64{task: ids[:1]})
	if err != nil {
		t.Fatal(err)
	}
	conv := runs[0].ConversationID
	defer s.m.pg.Exec(`DELETE FROM conversations WHERE id=$1`, conv)
	ctx := intercept.WithConvID(context.Background(), conv)
	input := json.RawMessage(fmt.Sprintf(`{"task_id":%d,"finding_id":%d,"severity":"low","status":"fixed"}`, task, ids[0]))
	for _, tool := range s.findingMutationTools() {
		result, err := tool.Call(ctx, input, nil)
		if err != nil || !result.IsError {
			t.Fatal("historical original mutation accepted", tool.Name(), result, err)
		}
	}
	f, err := s.m.pg.GetFinding(ids[0])
	if err != nil || f == nil || f.Severity != "high" || f.Status == "fixed" {
		t.Fatal("protected record changed", f, err)
	}
}
