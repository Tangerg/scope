package elasticsearch

import (
	jsonv2 "encoding/json/v2"
	"strings"
	"testing"
)

// Keep the existing keyword mapping policy for native searches. Core predicates
// use stored source and do not depend on keyword tokenization.
func TestMetadataStringsMapToKeyword(t *testing.T) {
	t.Parallel()

	template := metadataKeywordTemplate("metadata")
	rule, ok := template["metadata_strings_are_keywords"]
	if !ok {
		t.Fatalf("template = %#v, want a named rule", template)
	}
	if rule.PathMatch != "metadata.*" {
		t.Fatalf("PathMatch = %q, want metadata.*", rule.PathMatch)
	}
	if rule.MatchMappingType != "string" {
		t.Fatalf("MatchMappingType = %q, want string", rule.MatchMappingType)
	}
	if got := rule.Mapping["type"]; got != mappingTypeKeyword {
		t.Fatalf("mapping type = %v, want %q", got, mappingTypeKeyword)
	}
}

// The template has to reach the request body, and only strings may be
// redirected — numbers and booleans keep their detected types.
func TestCreateIndexRequestCarriesTheTemplate(t *testing.T) {
	t.Parallel()

	body, err := jsonv2.Marshal(indexMappings{
		DynamicTemplates: []map[string]dynamicTemplate{metadataKeywordTemplate("metadata")},
		Properties:       map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(body)
	for _, want := range []string{
		`"dynamic_templates"`,
		`"path_match":"metadata.*"`,
		`"match_mapping_type":"string"`,
		`"type":"keyword"`,
	} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("mappings = %s, want it to contain %s", encoded, want)
		}
	}
}
