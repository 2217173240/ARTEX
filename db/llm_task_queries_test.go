package db

import (
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

// 账本回归仅使用显式提供的独立 PostgreSQL，避免读取应用配置。
func ledgerQueryTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_LEDGER_TEST_DSN")
	if dsn == "" {
		t.Skip("set ARTEX_LEDGER_TEST_DSN to an isolated PostgreSQL database")
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	if err := sqlDB.Ping(); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("ledger_query_%d", time.Now().UnixNano())
	if _, err := sqlDB.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := sqlDB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Errorf("clean up isolated schema: %v", err)
		}
	})
	if _, err := sqlDB.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatal(err)
	}
	return &DB{sqlDB}
}

func TestTokenByModelTaskIdentity(t *testing.T) {
	d := ledgerQueryTestDB(t)
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO llm_usage
    (task_id, model, input_tokens, output_tokens, cache_read, cache_write) VALUES
    (NULL, 'shared', 2, 3, 4, 5),
    ('', 'shared', 7, 11, 13, 17),
    ('0', 'zero', 19, 23, 29, 31),
    ('task-a', 'alpha', 31, 2, 3, 4),
    ('task-a', 'alpha', 5, 7, 11, 13),
    ('task-a', 'beta', 70, 1, 2, 3),
    (' task-a ', 'spaced', 101, 103, 107, 109)`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		task string
		want []ModelTokenStat
	}{
		{"", []ModelTokenStat{{Model: "shared", Calls: 2, InputTokens: 9, OutputTokens: 14, CacheReadTokens: 17, CacheWriteTokens: 22}}},
		{"0", []ModelTokenStat{{Model: "zero", Calls: 1, InputTokens: 19, OutputTokens: 23, CacheReadTokens: 29, CacheWriteTokens: 31}}},
		{"task-a", []ModelTokenStat{
			{Model: "beta", Calls: 1, InputTokens: 70, OutputTokens: 1, CacheReadTokens: 2, CacheWriteTokens: 3},
			{Model: "alpha", Calls: 2, InputTokens: 36, OutputTokens: 9, CacheReadTokens: 14, CacheWriteTokens: 17},
		}},
		{" task-a ", []ModelTokenStat{{Model: "spaced", Calls: 1, InputTokens: 101, OutputTokens: 103, CacheReadTokens: 107, CacheWriteTokens: 109}}},
		{"missing", []ModelTokenStat{}},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("task=%q", tc.task), func(t *testing.T) {
			got, err := d.TokenByModel(tc.task)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("TokenByModel(%q)=%+v, want %+v", tc.task, got, tc.want)
			}
		})
	}
}

func TestListLLMRecordsTaskFilters(t *testing.T) {
	d := ledgerQueryTestDB(t)
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO llm_records (task_id, model, session_id) VALUES
    (NULL, 'alpha', 'main'), ('', 'alpha', 'main'), ('0', 'zero', 'main'),
    ('task-a', 'alpha', 'session-MAIN'), ('task-a', 'beta', 'other'),
    ('task-a', 'alpha', 'main-extra'), ('task-b', 'alpha', 'main'),
    (' task-a ', 'alpha', 'main')`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, model, session, task string
		page, size, total          int
		ids                        []int64
	}{
		{name: "unfiltered", size: 50, total: 8, ids: []int64{8, 7, 6, 5, 4, 3, 2, 1}},
		{name: "default pagination", page: -1, size: 0, total: 8, ids: []int64{8, 7, 6, 5, 4, 3, 2, 1}},
		{name: "task", task: "task-a", size: 50, total: 3, ids: []int64{6, 5, 4}},
		{name: "zero", task: "0", size: 50, total: 1, ids: []int64{3}},
		{name: "whitespace", task: " task-a ", size: 50, total: 1, ids: []int64{8}},
		{name: "missing", task: "missing", size: 50, ids: []int64{}},
		{name: "combined first page", model: "alpha", session: "MAIN", task: "task-a", size: 1, total: 2, ids: []int64{6}},
		{name: "combined second page", model: "alpha", session: "MAIN", task: "task-a", page: 1, size: 1, total: 2, ids: []int64{4}},
		{name: "combined exhausted page", model: "alpha", session: "MAIN", task: "task-a", page: 2, size: 1, total: 2, ids: []int64{}},
		{name: "combined no match", model: "beta", session: "MAIN", task: "task-a", size: 50, ids: []int64{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records, total, err := d.ListLLMRecords(tc.model, tc.session, tc.task, tc.page, tc.size)
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]int64, 0, len(records))
			for _, record := range records {
				ids = append(ids, record.ID)
				if tc.task != "" && record.TaskID != tc.task {
					t.Errorf("record %d task=%q, want %q", record.ID, record.TaskID, tc.task)
				}
			}
			if total != tc.total || !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("total=%d ids=%v, want total=%d ids=%v", total, ids, tc.total, tc.ids)
			}
		})
	}
}

func TestLLMRecordsTaskIndexMigration(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			d := ledgerQueryTestDB(t)
			if legacy {
				if _, err := d.Exec(`CREATE TABLE llm_records (
    id BIGSERIAL PRIMARY KEY, ts TIMESTAMPTZ DEFAULT now(), model TEXT,
    session_id TEXT, latency_ms INTEGER, input_tokens INTEGER,
    output_tokens INTEGER, cache_read INTEGER, cache_write INTEGER,
    status TEXT, error TEXT, request_body TEXT, response_body TEXT
);
INSERT INTO llm_records(model, session_id) VALUES ('legacy-model', 'legacy-session')`); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				if err := d.EnsureLLMRecordsTable(); err != nil {
					t.Fatalf("ensure run %d: %v", i+1, err)
				}
			}
			var indexes int
			if err := d.QueryRow(`SELECT count(*) FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relname = 'idx_llm_records_task'
  AND i.indrelid = 'llm_records'::regclass AND i.indisvalid
  AND pg_get_expr(i.indpred, i.indrelid) = '(task_id IS NOT NULL)'
  AND pg_get_indexdef(i.indexrelid, 1, true) = 'task_id'`).Scan(&indexes); err != nil {
				t.Fatal(err)
			}
			if indexes != 1 {
				t.Fatalf("valid task partial indexes=%d, want 1", indexes)
			}
			if legacy {
				var model string
				var task sql.NullString
				if err := d.QueryRow(`SELECT model, task_id FROM llm_records WHERE session_id='legacy-session'`).Scan(&model, &task); err != nil {
					t.Fatal(err)
				}
				if model != "legacy-model" || task.Valid {
					t.Fatalf("legacy row model=%q task=%+v", model, task)
				}
			}
			if err := d.InsertLLMRecord(&LLMRecord{TaskID: "task-after-migration", Model: "new-model"}); err != nil {
				t.Fatal(err)
			}
			records, total, err := d.ListLLMRecords("", "", "task-after-migration", 0, 50)
			if err != nil || total != 1 || len(records) != 1 || records[0].Model != "new-model" {
				t.Fatalf("migrated round trip records=%+v total=%d err=%v", records, total, err)
			}
		})
	}
}
