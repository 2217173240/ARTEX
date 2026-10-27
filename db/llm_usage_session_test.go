package db

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func useUsageFixtureDSN(t *testing.T) {
	t.Helper()
	if os.Getenv("ARTEX_LEDGER_TEST_DSN") == "" {
		t.Setenv("ARTEX_LEDGER_TEST_DSN", os.Getenv("ARTEX_PG_DSN"))
	}
}

// 此测试显式写入数据库输入值，不代表模型消耗。
func TestLLMUsageSessionMigrationAndDiagnostics(t *testing.T) {
	useUsageFixtureDSN(t)
	d := ledgerQueryTestDB(t)
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`ALTER TABLE llm_usage DROP COLUMN session_id`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO llm_usage(task_id,input_tokens) VALUES ('42',7)`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := d.EnsureLLMUsageTable(); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []LLMUsage{{TaskID: "42", SessionID: "same", InputTokens: 11}, {TaskID: "43", SessionID: "same", InputTokens: 99}, {TaskID: "42", SessionID: "second", InputTokens: 13}} {
		if err := d.InsertLLMUsage(&u); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(`UPDATE llm_usage SET ts='2026-10-27T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	rows, total, err := d.ListLLMUsage(t.Context(), "42", nil, 0, 2)
	if err != nil || total != 3 || len(rows) != 2 || rows[0].SessionID != "second" || rows[1].SessionID != "same" || rows[0].ID <= rows[1].ID {
		t.Fatalf("rows=%+v total=%d err=%v", rows, total, err)
	}
	rows, total, err = d.ListLLMUsage(t.Context(), "42", nil, 10, 2)
	if err != nil || total != 3 || rows == nil || len(rows) != 0 {
		t.Fatalf("empty page=%+v/%d %v", rows, total, err)
	}
	for session, want := range map[string]int{"same": 11, "": 7, "missing": 0} {
		rows, total, err = d.ListLLMUsage(t.Context(), "42", &session, 0, 50)
		if err != nil || (want == 0 && total != 0) || (want != 0 && (total != 1 || rows[0].InputTokens != want)) {
			t.Fatalf("session=%q rows=%+v/%d %v", session, rows, total, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := d.ListLLMUsage(ctx, "42", nil, 0, 50); err == nil {
		t.Fatal("canceled query succeeded")
	}
	for _, task := range []string{"", "0", "-1", "042", "+42", " 42", "9223372036854775808"} {
		if _, _, err := d.ListLLMUsage(t.Context(), task, nil, 0, 50); err == nil {
			t.Fatalf("accepted %q", task)
		}
	}
}

func TestLLMUsageArchiveNullableSessionCompatibility(t *testing.T) {
	useUsageFixtureDSN(t)
	d := ledgerQueryTestDB(t)
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// 实际归档还原路径使用相同 JSON 复合类型导入，缺失新增字段恢复为 NULL。
	raw := json.RawMessage(`[{"id":101,"ts":"2026-10-27T00:00:00Z","task_id":"42","input_tokens":7,"output_tokens":0,"cache_read":0,"cache_write":0},{"id":102,"ts":"2026-10-27T00:00:00Z","task_id":"42","session_id":"conv-kept","input_tokens":11,"output_tokens":0,"cache_read":0,"cache_write":0}]`)
	if err := insertArchiveRows(tx, "llm_usage", raw); err != nil {
		t.Fatal(err)
	}
	var absent bool
	var session string
	if err := tx.QueryRow(`SELECT session_id IS NULL FROM llm_usage WHERE id=101`).Scan(&absent); err != nil || !absent {
		t.Fatalf("old row=%v %v", absent, err)
	}
	if err := tx.QueryRow(`SELECT session_id FROM llm_usage WHERE id=102`).Scan(&session); err != nil || session != "conv-kept" {
		t.Fatalf("new row=%q %v", session, err)
	}
	var exported string
	if err := tx.QueryRow(`SELECT row_to_json(u)::text FROM llm_usage u WHERE id=102`).Scan(&exported); err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(exported), &row); err != nil {
		t.Fatal(err)
	}
	if row["session_id"] != "conv-kept" || TaskArchiveFormatVersion != 4 {
		t.Fatalf("export=%v version=%d", row, TaskArchiveFormatVersion)
	}
}
