package manifest

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponseSchemaValidation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		schema string
		valid  bool
	}{
		{"empty schema", `{}`, true},
		{"collection", `{"type":"object","required":["items"],"properties":{"items":{"type":"array"}}}`, true},
		{"local reference", `{"$defs":{"collection":{"type":"array"}},"properties":{"items":{"$ref":"#/$defs/collection"}}}`, true},
		{"draft 7", `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object"}`, true},
		{"invalid type", `{"type":"synthetic-sensitive-marker"}`, false},
		{"invalid nested type", `{"properties":{"items":{"items":{"type":"synthetic-sensitive-marker"}}}}`, false},
		{"invalid union type", `{"type":["array","synthetic-sensitive-marker"]}`, false},
		{"duplicate union type", `{"type":["array","array"]}`, false},
		{"empty union type", `{"type":[]}`, false},
		{"union type", `{"type":["array","null"]}`, true},
		{"literal type in default", `{"type":"object","default":{"type":"synthetic-sensitive-marker"}}`, true},
		{"unsupported dialect", `{"$schema":"https://synthetic-sensitive-marker.invalid/schema"}`, false},
		{"invalid keyword type", `{"required":"synthetic-sensitive-marker"}`, false},
		{"invalid pattern", `{"pattern":"["}`, false},
		{"remote reference", `{"$ref":"https://synthetic-sensitive-marker.invalid/schema"}`, false},
		{"unresolved local reference", `{"$ref":"#/$defs/missing"}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tool := Tool{Name: "read", Tier: TierUser, Type: ToolTypeRendered, RequestTemplate: "{}", ResponseTemplate: "ok"}
			if err := json.Unmarshal([]byte(tt.schema), &tool.ResponseSchema); err != nil {
				t.Fatal(err)
			}
			err := tool.Validate()
			if (err == nil) != tt.valid {
				t.Fatalf("Validate() = %v, want valid=%t", err, tt.valid)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-sensitive-marker") {
				t.Fatal("configuration diagnostic exposed a schema value")
			}
		})
	}
	tool := Tool{Name: "relay", Tier: TierUser, Type: ToolTypePassthrough, UpstreamToolName: "read", ResponseSchema: map[string]any{}}
	if err := tool.Validate(); err == nil {
		t.Fatal("passthrough accepted responseSchema")
	}
}

func TestValidateResponseDoesNotApplyDefaultsOrExposeValues(t *testing.T) {
	tool := Tool{Name: "read", Tier: TierUser, Type: ToolTypeRendered, RequestTemplate: "{}", ResponseTemplate: "ok"}
	if err := json.Unmarshal([]byte(`{
		"type":"object","required":["items"],
		"properties":{"items":{"type":"array","default":[],"items":{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}}}
	}`), &tool.ResponseSchema); err != nil {
		t.Fatal(err)
	}
	if err := tool.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{}`, `{"items":"synthetic-sensitive-marker"}`, `{"items":[{"name":17}]}`} {
		var value map[string]any
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			t.Fatal(err)
		}
		err := tool.ValidateResponse(value)
		if err == nil || err.Error() != "upstream response does not match responseSchema" {
			t.Fatalf("ValidateResponse() = %v, want safe validation error", err)
		}
		if body == `{}` && len(value) != 0 {
			t.Fatal("response validation applied defaults")
		}
	}
	if err := tool.ValidateResponse(map[string]any{"items": []any{}}); err != nil {
		t.Fatal(err)
	}
	// Revalidation after a manifest edit must replace the resolved validator.
	tool.ResponseSchema = map[string]any{"type": "string"}
	if err := tool.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := tool.ValidateResponse("ok"); err != nil {
		t.Fatal(err)
	}
	tool.ResponseSchema = map[string]any{"type": []string{"array", "null"}}
	if err := tool.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := tool.ValidateResponse(nil); err != nil {
		t.Fatal(err)
	}
	legacy := Tool{}
	if err := legacy.ValidateResponse(nil); err != nil {
		t.Fatal(err)
	}
}
