package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestRecordFactInputRejectsBeforeStore(t *testing.T) {
	inputs := []string{`null`, `[]`, `{`, `{"facts":null}`, `{"facts":[]}`, `{"facts":{}}`, `{"facts":"bad"}`, `{"facts":1}`}
	for _, field := range []string{"summary", "detail", "evidence", "confidence", "asset_ids", "observation_ids", "conclusion_kind", "context_key", "audit_id"} {
		for _, value := range []string{`null`, `0`, `""`} {
			inputs = append(inputs, fmt.Sprintf(`{"facts":[{"summary":"valid"}],%q:%s}`, field, value))
		}
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			if _, _, _, err := parseRecordFactInput(json.RawMessage(input)); err == nil {
				t.Fatal("invalid input accepted without a store")
			}
		})
	}
}

func factToolFixture(t *testing.T) (*db.DB, *db.ExplorationStore, *ToolSet, int64) {
	t.Helper()
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("fact tool input fixture", "local", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.DeleteTask(task.ID) })
	es := d.Exploration(task.ExplorationID)
	intent, err := es.AddIntent(map[string]any{"summary": "owner"}, 1, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	ts := NewToolSet(es, "test")
	ts.SetTaskID(task.ID)
	ts.SetOwnerNode(intent)
	return d, es, ts, intent
}

func TestRecordFactInputRealPostgres(t *testing.T) {
	d, es, ts, owner := factToolFixture(t)
	other, err := es.AddIntent(map[string]any{"summary": "override"}, 1, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	obs := evidenceNode(t, evidenceCall(t, ts.recordObservation(), map[string]any{"summary": "limited", "context_key": "local/input"}, false))
	claim := map[string]any{"summary": "limited claim", "conclusion_kind": "inconclusive", "context_key": "local/input", "observation_ids": []int64{obs.ID}}
	audit := evidenceNode(t, evidenceCall(t, ts.auditFactValidity(), claim, false))
	claim["audit_id"] = audit.ID
	before := ts.Writes()
	var factsBefore int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=(SELECT exploration_id FROM exploration_nodes WHERE id=$1) AND kind='fact'`, owner).Scan(&factsBefore); err != nil {
		t.Fatal(err)
	}
	for _, input := range []map[string]any{{"facts": nil, "summary": "fallback"}, {"facts": []any{}, "summary": "fallback"}, {"facts": []any{claim}, "audit_id": nil}} {
		evidenceCall(t, ts.recordFact(), input, true)
	}
	for _, invalid := range []any{nil, 0, -1, "bad"} {
		evidenceCall(t, ts.recordFact(), map[string]any{"intent_id": invalid, "facts": []any{map[string]any{"summary": "invalid default"}}}, true)
	}
	var factsAfter, auditUses int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=(SELECT exploration_id FROM exploration_nodes WHERE id=$1) AND kind='fact'`, owner).Scan(&factsAfter); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE src_id=$1 AND rel='qualifies'`, audit.ID).Scan(&auditUses); err != nil {
		t.Fatal(err)
	}
	current, err := es.GetNode(audit.ID)
	if err != nil {
		t.Fatal(err)
	}
	var originalPayload, currentPayload any
	if err := json.Unmarshal(audit.Payload, &originalPayload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(current.Payload, &currentPayload); err != nil {
		t.Fatal(err)
	}
	if factsAfter != factsBefore || ts.Writes() != before || auditUses != 0 || current.State != audit.State || !reflect.DeepEqual(originalPayload, currentPayload) {
		t.Fatal("rejected input changed facts, writes or audit")
	}
	evidenceCall(t, ts.recordFact(), map[string]any{"facts": []any{claim}}, false)
	var out struct {
		IDs    []int64           `json:"ids"`
		Errors map[string]string `json:"errors"`
	}
	raw := evidenceCall(t, ts.recordFact(), map[string]any{"intent_id": owner, "facts": []any{map[string]any{"summary": "default"}, map[string]any{"summary": ""}, map[string]any{"summary": "override", "intent_id": other}}}, false)
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.IDs) != 3 || out.IDs[0] <= 0 || out.IDs[1] != 0 || out.IDs[2] <= 0 || len(out.Errors) != 1 || out.Errors["1"] == "" {
		t.Fatal(raw)
	}
	for _, pair := range [][2]int64{{owner, out.IDs[0]}, {other, out.IDs[2]}} {
		ids, err := es.FactsYielded(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, id := range ids {
			if id == pair[1] {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing intent edge %v", pair)
		}
	}
	legacy := evidenceCall(t, ts.recordFact(), map[string]any{"summary": "legacy"}, false)
	if !strings.HasPrefix(legacy, "fact recorded: ") || strings.Contains(legacy, "ids") {
		t.Fatal(legacy)
	}
	var legacyID int64
	if _, err := fmt.Sscanf(legacy, "fact recorded: %d", &legacyID); err != nil {
		t.Fatal(err)
	}
	var ownerEdge bool
	if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM exploration_edges WHERE src_id=$1 AND dst_id=$2 AND rel='yields')`, owner, legacyID).Scan(&ownerEdge); err != nil {
		t.Fatal(err)
	}
	if !ownerEdge {
		t.Fatal("legacy call did not use ownerNode")
	}
	if ts.Writes().Facts-before.Facts != 4 {
		t.Fatal(ts.Writes())
	}
}
