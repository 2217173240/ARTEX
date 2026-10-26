package llmrec

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmpool"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
	"github.com/google/uuid"
)

func TestAttributionContextResolution(t *testing.T) {
	ctx := WithExplorationID(WithWorker(WithTaskID(t.Context(), "42"), "reporter"), 99)
	for _, id := range []int64{0, -1} {
		if got := ExplorationIDFrom(WithExplorationID(ctx, id)); got != 99 {
			t.Fatalf("invalid exploration changed parent: %d", got)
		}
	}
	if ExplorationIDFrom(nil) != 0 {
		t.Fatal("nil context has exploration")
	}
	for _, tc := range []struct {
		ctx                   context.Context
		session, task, worker string
		exp                   int64
	}{
		{ctx, "conv-123", "42", "reporter", 99},
		{ctx, "exp12-worker-i1", "42", "reporter", 99},
		{WithWorker(ctx, "judge"), "conv-123", "42", "judge", 99},
		{t.Context(), "exp12-worker-i1", "12", "worker", 12},
		{WithTaskID(t.Context(), "42"), "exp12-worker-i1", "42", "worker", 12},
		{t.Context(), "conv-123", "", "", 0},
	} {
		task, exp, worker := attributionFrom(tc.ctx, tc.session)
		if task != tc.task || exp != tc.exp || worker != tc.worker {
			t.Fatalf("%s attribution=(%q,%d,%q), want (%q,%d,%q)", tc.session, task, exp, worker, tc.task, tc.exp, tc.worker)
		}
	}
}

func TestExplicitTaskOwnerProvenance(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int64
	}{{"42", 42}, {" 42 ", 42}, {"", 0}, {"exp42", 0}, {"0", 0}, {"-1", 0}, {"042", 0}, {"+42", 0}, {"9223372036854775808", 0}} {
		if got := explicitTaskOwner(WithTaskID(t.Context(), tc.value)); got != tc.want {
			t.Fatalf("%q owner=%d want %d", tc.value, got, tc.want)
		}
	}
	if explicitTaskOwner(transcript.WithSessionID(t.Context(), "exp42-worker")) != 0 {
		t.Fatal("legacy session inferred registry owner")
	}
}

func TestAttributionLateBodiesRespectTaskLifecycle(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err := pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	if err := pg.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		for _, state := range []string{"deleted", "archived", "soft-deleted", "legacy", "nonnumeric", "taskless"} {
			t.Run(fmt.Sprintf("stream=%v/state=%s", stream, state), func(t *testing.T) {
				ownedTask, err := pg.CreateTask("late attribution", "verify body lifecycle", nil, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				taskID := strconv.FormatInt(ownedTask.ID, 10)
				profile := "late-attribution-" + uuid.NewString()
				session := "conv-" + uuid.NewString()
				ctx := transcript.WithSessionID(t.Context(), session)
				ctx = WithTaskID(ctx, taskID)
				switch state {
				case "deleted":
					if _, err := pg.Exec(`DELETE FROM tasks WHERE id=$1`, ownedTask.ID); err != nil {
						t.Fatal(err)
					}
				case "archived":
					if _, err := pg.Exec(`UPDATE tasks SET archived_at=now() WHERE id=$1`, ownedTask.ID); err != nil {
						t.Fatal(err)
					}
				case "soft-deleted":
					if _, err := pg.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, ownedTask.ID); err != nil {
						t.Fatal(err)
					}
				case "legacy":
					if _, err := pg.Exec(`DELETE FROM tasks WHERE id=$1`, ownedTask.ID); err != nil {
						t.Fatal(err)
					}
					session = "exp" + taskID + "-worker"
					ctx = transcript.WithSessionID(t.Context(), session)
				case "nonnumeric":
					taskID = "external-" + uuid.NewString()
					ctx = WithTaskID(transcript.WithSessionID(t.Context(), session), taskID)
				case "taskless":
					ctx = transcript.WithSessionID(t.Context(), session)
				}
				defer func() {
					if _, err := pg.Exec(`DELETE FROM llm_records WHERE session_id=$1`, session); err != nil {
						t.Error(err)
					}
					if _, err := pg.Exec(`DELETE FROM llm_usage WHERE profile_name=$1`, profile); err != nil {
						t.Error(err)
					}
					if _, err := pg.Exec(`DELETE FROM tasks WHERE id=$1`, ownedTask.ID); err != nil {
						t.Error(err)
					}
					if _, err := pg.Exec(`DELETE FROM explorations WHERE id=$1`, ownedTask.ExplorationID); err != nil {
						t.Error(err)
					}
				}()
				provider, err := llm.NewProvider(llm.Config{Format: llm.FormatAnthropic, BaseURL: "http://127.0.0.1:1", APIKey: "local-test", Model: "late-attribution-test"})
				if err != nil {
					t.Fatal(err)
				}
				r := Wrap(provider, pg, "late-attribution-test", profile, "", "", nil)
				ctx, cancel := context.WithCancel(ctx)
				cancel()
				req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("local cancellation")}, MaxTokens: 8}
				if stream {
					var callErr error
					for _, err := range r.Stream(ctx, req) {
						if err != nil {
							callErr = err
						}
					}
					if callErr == nil {
						t.Fatal("canceled stream succeeded")
					}
				} else if _, _, _, err := r.Complete(ctx, req); err == nil {
					t.Fatal("canceled completion succeeded")
				}
				var bodies, usage int
				if err := pg.QueryRow(`SELECT count(*) FROM llm_records WHERE session_id=$1`, session).Scan(&bodies); err != nil {
					t.Fatal(err)
				}
				if err := pg.QueryRow(`SELECT count(*) FROM llm_usage WHERE profile_name=$1`, profile).Scan(&usage); err != nil {
					t.Fatal(err)
				}
				want := 0
				if state == "legacy" || state == "nonnumeric" || state == "taskless" {
					want = 1
				}
				if bodies != want || usage != 1 {
					t.Fatalf("bodies=%d want %d; historical usage=%d want 1", bodies, want, usage)
				}
			})
		}
	}
}

func TestAttributionRealProviderCanceledCalls(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err := pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	if err := pg.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		for _, bodies := range []bool{false, true} {
			for _, pooled := range []bool{false, true} {
				t.Run(fmt.Sprintf("stream=%v/bodies=%v/pool=%v", stream, bodies, pooled), func(t *testing.T) {
					profile := "attribution-" + uuid.NewString()
					ownedTask, err := pg.CreateTask("attribution", "verify real recorder ownership", nil, 0, 0)
					if err != nil {
						t.Fatal(err)
					}
					taskID := strconv.FormatInt(ownedTask.ID, 10)
					defer func() {
						if _, err := pg.Exec(`DELETE FROM llm_usage WHERE profile_name=$1`, profile); err != nil {
							t.Error(err)
						}
						if _, err := pg.Exec(`DELETE FROM llm_records WHERE task_id=$1`, taskID); err != nil {
							t.Error(err)
						}
						if _, err := pg.Exec(`DELETE FROM tasks WHERE id=$1`, ownedTask.ID); err != nil {
							t.Error(err)
						}
						if _, err := pg.Exec(`DELETE FROM explorations WHERE id=$1`, ownedTask.ExplorationID); err != nil {
							t.Error(err)
						}
					}()
					cfg := llm.Config{Format: llm.FormatAnthropic, BaseURL: "http://127.0.0.1:1", APIKey: "local-test", Model: "attribution-test"}
					provider, err := llm.NewProvider(cfg)
					if err != nil {
						t.Fatal(err)
					}
					r := Wrap(provider, pg, cfg.Model, profile, "", "", func() bool { return bodies })
					var caller llm.Provider = r
					if pooled {
						caller = llmpool.New([]*llmpool.Member{{ID: 1, Name: profile, Model: cfg.Model, Prov: r}}, nil)
					}
					ctx := WithExplorationID(WithWorker(WithTaskID(transcript.WithSessionID(t.Context(), "conv-123"), taskID), "reporter"), ownedTask.ExplorationID)
					ctx, cancel := context.WithCancel(ctx)
					cancel()
					req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("local cancellation")}, MaxTokens: 8}
					if stream {
						var callErr error
						for _, err := range caller.Stream(ctx, req) {
							if err != nil {
								callErr = err
							}
						}
						if callErr == nil {
							t.Fatal("canceled real stream succeeded")
						}
					} else {
						if _, _, _, err := caller.Complete(ctx, req); err == nil {
							t.Fatal("canceled real completion succeeded")
						}
					}
					var task, worker, status string
					var exp, count int64
					if err := pg.QueryRow(`SELECT count(*),max(task_id),max(exploration_id),max(worker),max(status) FROM llm_usage WHERE profile_name=$1`, profile).Scan(&count, &task, &exp, &worker, &status); err != nil {
						t.Fatal(err)
					}
					if count != 1 || task != taskID || exp != ownedTask.ExplorationID || worker != "reporter" || status != "error" {
						t.Fatalf("ledger=(%d,%q,%d,%q,%q)", count, task, exp, worker, status)
					}
					records, total, err := pg.ListLLMRecords("", "", taskID, 0, 50)
					if err != nil {
						t.Fatal(err)
					}
					want := 0
					if bodies {
						want = 1
					}
					if total != want || len(records) != want {
						t.Fatalf("body records=%d/%d want %d", total, len(records), want)
					}
					if bodies && (records[0].Worker != "reporter" || records[0].SessionID != "conv-123" || records[0].Status != "error") {
						t.Fatalf("record=%+v", records[0])
					}
					// 输入值验证真实台账聚合，不代表模型实际消耗。
					r.recordUsage(taskID, ownedTask.ExplorationID, "reporter", llm.Usage{InputTokens: 7, OutputTokens: 3}, 1, "ok")
					stats, err := pg.TokenByModel(taskID)
					if err != nil {
						t.Fatal(err)
					}
					if len(stats) != 1 || stats[0].InputTokens != 7 || stats[0].OutputTokens != 3 || stats[0].Calls != 2 {
						t.Fatalf("token aggregation=%+v", stats)
					}
				})
			}
		}
	}
}
