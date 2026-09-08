package opensearch

import (
	"encoding/json"
	"strings"
	"testing"
)

// Metadata keys are unknown when the index is created, so their fields map
// dynamically — and the default for a JSON string is "text with a .keyword
// sub-field", where the text field is analyzed. A filter compares whole values
// case-sensitively, so `metadata.author:"Alice"` reaching the analyzed field
// would match an author of "Alice Smith" or "alice" and disagree with
// filter.Match. The dynamic template maps the metadata path to keyword so the
// field the compiler queries is the exact-match one.
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

	body, err := json.Marshal(indexMappings{
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
