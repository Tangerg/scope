package moderation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/moderation"
)

func TestModelFunc(t *testing.T) {
	want := errors.New("boom")
	model := moderation.ModelFunc(func(_ context.Context, request *moderation.Request) (*moderation.Response, error) {
		if len(request.Texts) != 1 || request.Texts[0] != "hello" {
			t.Fatalf("texts = %#v", request.Texts)
		}
		return nil, want
	})
	request, _ := moderation.NewRequest([]string{"hello"})
	if _, err := model.Call(t.Context(), request); !errors.Is(err, want) {
		t.Fatalf("Call error = %v, want %v", err, want)
	}
}

func TestOptionsAndRequestValidation(t *testing.T) {
	if err := (moderation.Options{Model: " model "}).Validate(); err == nil {
		t.Fatal("Options accepted model with surrounding whitespace")
	}
	if _, err := moderation.NewRequest(nil); err == nil {
		t.Fatal("NewRequest accepted empty texts")
	}
	if _, err := moderation.NewRequest([]string{"valid", ""}); err == nil {
		t.Fatal("NewRequest accepted an empty text entry")
	}
	if _, err := (moderation.Options{}).Resolve(moderation.Options{}); !errors.Is(err, moderation.ErrInvalidOptions) {
		t.Fatalf("Resolve without a model = %v; want ErrInvalidOptions", err)
	}
	if err := (*moderation.Request)(nil).Validate(); err == nil {
		t.Fatal("Validate accepted nil request")
	}
	invalid := &moderation.Request{Texts: []string{"text"}}
	invalid.Options = moderation.Options{Model: " model "}
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate accepted model with surrounding whitespace")
	}
	options := new(moderation.Options)
	if err := options.Extensions.Set("provider/value", func() {}); err == nil || !options.Extensions.IsZero() {
		t.Fatalf("failed SetExtension mutated options: %#v, %v", options.Extensions, err)
	}
	if _, err := (moderation.Options{Model: " model "}).Resolve(moderation.Options{}); err == nil {
		t.Fatal("Resolve accepted invalid base options")
	}
}

func TestCategoriesAndResponse(t *testing.T) {
	categories := moderation.Categories{"hate": {}}
	if categories.Flagged() {
		t.Fatal("zero Categories is flagged")
	}
	categories["provider/new_category"] = moderation.Verdict{Flagged: true, Score: 0.75}
	if !categories.Flagged() {
		t.Fatal("Flagged did not aggregate Hate")
	}
	output, err := moderation.NewOutput(categories, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := moderation.NewResponse([]*moderation.Output{output}, &moderation.ResponseMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	if response.First() != output {
		t.Fatal("First did not return first output")
	}
	categories["provider/new_category"] = moderation.Verdict{}
	if !output.Categories["provider/new_category"].Flagged {
		t.Fatal("NewOutput aliases caller categories")
	}
}

func TestOptionsResolveAndCopies(t *testing.T) {
	base := moderation.Options{Model: "base", Extensions: mustExtensions(t, map[string]any{"provider/base": true})}
	resolved, err := base.Resolve(moderation.Options{
		Model:      "override",
		Extensions: mustExtensions(t, map[string]any{"provider/override": true}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "override" ||
		!mustDecode[bool](t, resolved.Extensions, "provider/base") ||
		!mustDecode[bool](t, resolved.Extensions, "provider/override") {
		t.Fatalf("Resolve = %#v", resolved)
	}
	clone := resolved.Clone()
	if err := clone.Extensions.Set("provider/base", false); err != nil {
		t.Fatal(err)
	}
	if !mustDecode[bool](t, resolved.Extensions, "provider/base") {
		t.Fatal("Options.Clone aliases source state")
	}
}

func mustExtensions(t *testing.T, values map[string]any) metadata.Extensions {
	t.Helper()
	var output metadata.Extensions
	for key, value := range values {
		if err := output.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	return output
}

func mustDecode[T any](t *testing.T, values metadata.Extensions, key string) T {
	t.Helper()
	value, ok, err := values.Decode[T](key)
	if err != nil || !ok {
		t.Fatalf("metadata.Decode(%q) = %#v, %t, %v", key, value, ok, err)
	}
	return value
}

func TestCategoriesRemainOpen(t *testing.T) {
	categories := moderation.Categories{"future/provider_category": {Flagged: true, Score: 1}}
	if !categories.Flagged() {
		t.Fatal("provider category was not aggregated")
	}
	if moderation.Categories(nil).Flagged() {
		t.Fatal("nil Categories is flagged")
	}
}

func TestResponseConstructorsRejectInvalidValues(t *testing.T) {
	categories := moderation.Categories{"safe": {}}
	if _, err := moderation.NewOutput(nil, nil); err == nil {
		t.Fatal("NewOutput accepted empty categories")
	}
	for name, verdict := range map[string]moderation.Verdict{
		"negative": {Score: -0.1},
		"too high": {Score: 1.1},
	} {
		if _, err := moderation.NewOutput(moderation.Categories{name: verdict}, nil); err == nil {
			t.Fatalf("NewOutput accepted %s score", name)
		}
	}
	output, _ := moderation.NewOutput(categories, nil)
	if _, err := moderation.NewResponse(nil, &moderation.ResponseMetadata{}); err == nil {
		t.Fatal("NewResponse accepted no outputs")
	}
	if _, err := moderation.NewResponse([]*moderation.Output{output}, nil); err != nil {
		t.Fatalf("NewResponse rejected optional metadata: %v", err)
	}
	if (&moderation.Response{}).First() != nil || (*moderation.Response)(nil).First() != nil {
		t.Fatal("empty response returned a output")
	}
}
