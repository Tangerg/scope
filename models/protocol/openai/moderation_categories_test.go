package openai

import (
	"reflect"
	"strings"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"
)

// The mapping names every moderation category one field at a time, so a
// category OpenAI adds arrives as a field nobody reads and a flagged input
// comes back unflagged for it — the failure would be silence, in the one place
// where silence is the whole problem.
//
// The provider's set is a Go type here, so it can be asked directly. Every JSON
// name the SDK declares on ModerationCategories must appear in the response
// this builds, and nothing else may.
func TestModerationCoversEverySDKCategory(t *testing.T) {
	t.Parallel()

	declared := sdkModerationCategoryNames(t)
	mapped := builtModerationCategoryNames(t)

	for _, name := range declared {
		if _, found := mapped[name]; !found {
			t.Errorf("SDK declares category %q and the mapping does not report it", name)
		}
	}
	for name := range mapped {
		if !containsName(declared, name) {
			t.Errorf("the mapping reports category %q that the SDK does not declare", name)
		}
	}
	if len(mapped) != len(declared) {
		t.Fatalf("mapped %d categories, SDK declares %d", len(mapped), len(declared))
	}
}

// sdkModerationCategoryNames reads the JSON names off the SDK struct and
// normalizes them the way the mapping does: OpenAI writes a subcategory as
// "harassment/threatening" and "self-harm", while a Core category key uses
// underscores throughout.
func sdkModerationCategoryNames(t *testing.T) []string {
	t.Helper()

	structType := reflect.TypeOf(openaisdk.ModerationCategories{})
	names := make([]string, 0, structType.NumField())
	for index := range structType.NumField() {
		field := structType.Field(index)
		tag := field.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		// The SDK carries response metadata in a field with no category name;
		// it is not a category.
		if name == "" || name == "-" {
			continue
		}
		names = append(names, strings.ReplaceAll(strings.ReplaceAll(name, "/", "_"), "-", "_"))
	}
	if len(names) == 0 {
		t.Fatal("read no category names off openaisdk.ModerationCategories")
	}
	return names
}

func builtModerationCategoryNames(t *testing.T) map[string]struct{} {
	t.Helper()

	model := &ModerationModel{}
	response, err := model.buildModerationResponse(&openaisdk.ModerationNewResponse{
		ID:      "modr-1",
		Model:   "omni-moderation-latest",
		Results: []openaisdk.Moderation{{}},
	})
	if err != nil {
		t.Fatalf("buildModerationResponse() = %v, want nil", err)
	}
	if len(response.Outputs) != 1 {
		t.Fatalf("outputs = %d, want 1", len(response.Outputs))
	}
	names := make(map[string]struct{}, len(response.Outputs[0].Categories))
	for name := range response.Outputs[0].Categories {
		names[name] = struct{}{}
	}
	return names
}

func containsName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}
