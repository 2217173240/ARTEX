package db

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMergeToolSchemaPropertiesPreservesUserConfiguration(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	key := "fixture_optional_schema_upgrade"
	defer d.Exec(`DELETE FROM tools WHERE key=$1`, key)
	old := json.RawMessage(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string","description":"user wording","default":"user default"},"audit_id":{"type":"integer","description":"custom audit description","default":91}}}`)
	if err = d.SeedTool(key, "user description", old, json.RawMessage(`["custom-agent"]`)); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE tools SET enabled=false WHERE key=$1`, key); err != nil {
		t.Fatal(err)
	}
	properties := map[string]any{"observation_ids": map[string]any{"type": "array"}, "conclusion_kind": map[string]any{"type": "string"}, "context_key": map[string]any{"type": "string"}, "audit_id": map[string]any{"type": "integer"}}
	for i := 0; i < 2; i++ {
		if err = d.MergeToolSchemaProperties(key, properties); err != nil {
			t.Fatal(err)
		}
	}
	row, err := d.GetTool(key)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(row.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	if len(props) != 5 || props["audit_id"].(map[string]any)["default"] != float64(91) || props["summary"].(map[string]any)["default"] != "user default" {
		t.Fatal(string(row.Schema))
	}
	if row.Description != "user description" || row.Enabled || !reflect.DeepEqual(row.Agents, []string{"custom-agent"}) || !reflect.DeepEqual(schema["required"], []any{"summary"}) {
		t.Fatalf("user configuration changed %+v %v", row, schema)
	}
}
