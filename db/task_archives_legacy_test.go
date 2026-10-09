package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestTaskArchiveNodeDefaultsRejectNullRows(t *testing.T) {
	if err := insertArchiveRows(nil, "exploration_nodes", json.RawMessage(`[null]`)); err == nil {
		t.Fatal("a null archive row must fail before inserting graph data")
	}
}

func TestTaskArchiveRestoreNodeSchemaCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name         string
		version      int
		legacy       bool
		explicitNull bool
	}{
		{name: "v1_pre_digest", version: 1, legacy: true},
		{name: "v2_pre_digest", version: 2, legacy: true},
		{name: "current_preserves_version", version: TaskArchiveFormatVersion},
		{name: "explicit_null_rejected", version: TaskArchiveFormatVersion, explicitNull: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Open(testDSN(t))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if err := d.EnsureLLMRecordsTable(); err != nil {
				t.Fatal(err)
			}
			if err := d.EnsureLLMUsageTable(); err != nil {
				t.Fatal(err)
			}
			task, err := d.CreateTaskWithOptions("historical archive", "preserve task and graph", TaskCreateOptions{Name: tc.name})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := d.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID); err != nil {
					t.Error(err)
				}
				if err := d.DeleteTask(task.ID); err != nil {
					t.Error(err)
				}
			}()
			if err := d.SetPaused(task.ID, true); err != nil {
				t.Fatal(err)
			}
			nodeID, err := d.Exploration(task.ExplorationID).AddNode(KindFact,
				map[string]any{"summary": "legacy fact", "confidence": 0.75}, 4, "confirmed", "worker", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.Exec(`UPDATE exploration_nodes SET content_version=7 WHERE id=$1`, nodeID); err != nil {
				t.Fatal(err)
			}
			archive, err := d.QueueTaskArchive(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := d.ClaimTaskArchiveJob(t.Context())
			if err != nil || claimed == nil || claimed.ID != archive.ID || claimed.State != Archiving {
				t.Fatalf("archive claim=%+v, err=%v", claimed, err)
			}
			snapshot, err := d.SnapshotTaskArchive(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantTask, err := decodeArchiveRows(snapshot.Tables["tasks"])
			if err != nil {
				t.Fatal(err)
			}
			// The update trigger assigns a new timestamp when the stub is restored.
			delete(wantTask[0], "updated_at")
			wantNodes, err := decodeArchiveRows(snapshot.Tables["exploration_nodes"])
			if err != nil || len(wantNodes) < 2 {
				t.Fatalf("fixture nodes=%v, err=%v", wantNodes, err)
			}
			if err := d.CompleteTaskArchive(archive.ID, snapshot, "/tmp/legacy-schema-fixture.tar.zst", "fixture", 1, 1); err != nil {
				t.Fatal(err)
			}
			if live, err := d.GetTask(task.ID); err != nil || live != nil {
				t.Fatalf("cold task=%+v, err=%v", live, err)
			}
			var coldNodes int
			if err := d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, task.ExplorationID).Scan(&coldNodes); err != nil || coldNodes != 0 {
				t.Fatalf("cold nodes=%d, err=%v", coldNodes, err)
			}
			snapshot.FormatVersion = tc.version
			if tc.legacy {
				// v1/v2 shipped on 2026-08-28 (9da521e/0ec45bb), before these
				// node columns and side-question/evidence tables were introduced.
				for _, table := range []string{"side_question_sessions", "side_question_requests", "traffic_evidence_snapshots", "finding_traffic_bindings"} {
					delete(snapshot.Tables, table)
					delete(snapshot.DataCounts, table)
				}
				nodes, err := decodeArchiveRows(snapshot.Tables["exploration_nodes"])
				if err != nil {
					t.Fatal(err)
				}
				for i, row := range nodes {
					for _, field := range []string{"content_version", "cold_since_round", "delete_reason"} {
						delete(row, field)
					}
					wantNodes[i]["content_version"] = json.Number("0")
					wantNodes[i]["cold_since_round"] = nil
					wantNodes[i]["delete_reason"] = nil
				}
				snapshot.Tables["exploration_nodes"], err = json.Marshal(nodes)
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.explicitNull {
				nodes, err := decodeArchiveRows(snapshot.Tables["exploration_nodes"])
				if err != nil {
					t.Fatal(err)
				}
				nodes[0]["content_version"] = nil
				snapshot.Tables["exploration_nodes"], err = json.Marshal(nodes)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := d.QueueTaskArchiveRestore(archive.ID); err != nil {
				t.Fatal(err)
			}
			claimed, err = d.ClaimTaskArchiveJob(t.Context())
			if err != nil || claimed == nil || claimed.ID != archive.ID || claimed.State != Restoring {
				t.Fatalf("restore claim=%+v, err=%v", claimed, err)
			}
			_, err = d.RestoreTaskArchive(archive.ID, snapshot, 0)
			if tc.explicitNull {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23502" || pgErr.ColumnName != "content_version" {
					t.Fatalf("explicit null restore error=%v, want content_version NOT NULL violation", err)
				}
				if live, err := d.GetTask(task.ID); err != nil || live != nil {
					t.Fatalf("failed restore exposed task=%+v, err=%v", live, err)
				}
				if err := d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, task.ExplorationID).Scan(&coldNodes); err != nil || coldNodes != 0 {
					t.Fatalf("failed restore left nodes=%d, err=%v", coldNodes, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for table, want := range map[string][]map[string]any{"tasks": wantTask, "exploration_nodes": wantNodes} {
				column, id := "id", task.ID
				if table == "exploration_nodes" {
					column, id = "exploration_id", task.ExplorationID
				}
				raw, _, err := queryArchiveRows(d, fmt.Sprintf(`SELECT * FROM %s WHERE %s=$1 ORDER BY id`, table, column), id)
				if err != nil {
					t.Fatal(err)
				}
				got, err := decodeArchiveRows(raw)
				if err != nil {
					t.Fatal(err)
				}
				if table == "tasks" {
					delete(got[0], "updated_at")
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("restored %s values changed: got=%v want=%v", table, got, want)
				}
			}
			if err := d.CompleteTaskArchiveRestore(archive.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
