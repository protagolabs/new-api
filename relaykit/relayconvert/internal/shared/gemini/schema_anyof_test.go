package gemini

import (
	"encoding/json"
	"testing"
)

func cleanJSON(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	var params interface{}
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		t.Fatal(err)
	}
	cleaned, ok := CleanFunctionParameters(params).(map[string]interface{})
	if !ok {
		t.Fatalf("expected object, got %T", cleaned)
	}
	return cleaned
}

func prop(t *testing.T, schema map[string]interface{}, name string) map[string]interface{} {
	t.Helper()
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("no properties in %v", schema)
	}
	p, ok := props[name].(map[string]interface{})
	if !ok {
		t.Fatalf("no property %q in %v", name, props)
	}
	return p
}

// Optional[str] serializes to anyOf:[{type:string},{type:null}] with no top-level
// type. Google's functionDeclaration validator rejects any node without an explicit
// type ("... schema didn't specify the schema type field"), taking down the whole
// request. Regression guard for the api-proxy intermittent 400 on Gemini tool calls.
func TestAnyOfNullableCollapsesToNullableType(t *testing.T) {
	got := cleanJSON(t, `{
		"type": "object",
		"properties": {
			"target_artifact_id": {"anyOf": [{"type": "string"}, {"type": "null"}], "default": null},
			"content": {"type": "string"}
		},
		"required": ["content"]
	}`)

	target := prop(t, got, "target_artifact_id")
	if target["type"] != "STRING" {
		t.Errorf("type: want STRING, got %v", target["type"])
	}
	if target["nullable"] != true {
		t.Errorf("nullable: want true, got %v", target["nullable"])
	}
	if _, leftover := target["anyOf"]; leftover {
		t.Errorf("anyOf should be gone, got %v", target["anyOf"])
	}
	// The parent's own default must survive the hoist.
	if _, ok := target["default"]; !ok {
		t.Errorf("parent default was dropped: %v", target)
	}
}

// Every node must end up with a type, at any nesting depth.
func TestAnyOfNullableNestedObjectAndArray(t *testing.T) {
	got := cleanJSON(t, `{
		"type": "object",
		"properties": {
			"cfg": {
				"anyOf": [
					{"type": "object", "properties": {"tag": {"anyOf": [{"type": "string"}, {"type": "null"}]}}},
					{"type": "null"}
				]
			},
			"ids": {"anyOf": [{"type": "array", "items": {"type": "integer"}}, {"type": "null"}]}
		}
	}`)

	cfg := prop(t, got, "cfg")
	if cfg["type"] != "OBJECT" || cfg["nullable"] != true {
		t.Errorf("cfg: want OBJECT+nullable, got %v", cfg)
	}
	tag := prop(t, cfg, "tag")
	if tag["type"] != "STRING" || tag["nullable"] != true {
		t.Errorf("cfg.tag: want STRING+nullable, got %v", tag)
	}

	ids := prop(t, got, "ids")
	if ids["type"] != "ARRAY" || ids["nullable"] != true {
		t.Errorf("ids: want ARRAY+nullable, got %v", ids)
	}
	items, ok := ids["items"].(map[string]interface{})
	if !ok || items["type"] != "INTEGER" {
		t.Errorf("ids.items: want INTEGER, got %v", ids["items"])
	}
}

// A real union keeps anyOf (Google accepts it in place of type), but the type-less
// null branch must still go — that branch alone is enough to fail validation.
func TestAnyOfGenuineUnionKeepsBranchesDropsNull(t *testing.T) {
	got := cleanJSON(t, `{
		"type": "object",
		"properties": {
			"val": {"anyOf": [{"type": "string"}, {"type": "number"}, {"type": "null"}]}
		}
	}`)

	val := prop(t, got, "val")
	if val["nullable"] != true {
		t.Errorf("nullable: want true, got %v", val)
	}
	branches, ok := val["anyOf"].([]interface{})
	if !ok || len(branches) != 2 {
		t.Fatalf("want 2 remaining branches, got %v", val["anyOf"])
	}
	for _, b := range branches {
		bm := b.(map[string]interface{})
		if _, hasType := bm["type"]; !hasType {
			t.Errorf("branch without type survived: %v", bm)
		}
	}
}

// anyOf with a single non-null branch should also hoist rather than stay wrapped.
func TestAnyOfSingleBranchHoists(t *testing.T) {
	got := cleanJSON(t, `{
		"type": "object",
		"properties": {"only": {"anyOf": [{"type": "string", "format": "date-time"}]}}
	}`)

	only := prop(t, got, "only")
	if only["type"] != "STRING" {
		t.Errorf("type: want STRING, got %v", only["type"])
	}
	if only["format"] != "date-time" {
		t.Errorf("format should be hoisted, got %v", only["format"])
	}
	if _, leftover := only["anyOf"]; leftover {
		t.Errorf("anyOf should be gone, got %v", only)
	}
	if _, isNullable := only["nullable"]; isNullable {
		t.Errorf("no null branch, so nullable must not be set: %v", only)
	}
}
