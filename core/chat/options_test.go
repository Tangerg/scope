package chat_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/metadata"
)

func TestOptionsModel(t *testing.T) {
	options := chat.Options{Model: "model"}
	err := options.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if options.Model != "model" {
		t.Fatalf("Model = %q", options.Model)
	}
	if err := (chat.Options{Model: " model "}).Validate(); !errors.Is(err, chat.ErrInvalidOptions) {
		t.Fatalf("Options model error = %v", err)
	}
}

func TestOptionsZeroValueIsValid(t *testing.T) {
	var options chat.Options
	if err := options.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	encoded, err := jsonv2.Marshal(options)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(encoded) != `{}` {
		t.Fatalf("Options{} JSON = %s, want {}", encoded)
	}
}

func TestOptionsValidateBoundaries(t *testing.T) {
	options := chat.Options{
		Model:            "model",
		FrequencyPenalty: new(-2.0),
		MaxOutputTokens:  new(int64(1)),
		PresencePenalty:  new(2.0),
		ReasoningEffort:  "high",
		Stop:             []string{"stop"},
		Temperature:      new(0.0),
		TopK:             new(int64(1)),
		TopP:             new(1.0),
	}
	if err := options.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	encoded, err := jsonv2.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	var got chat.Options
	if err := jsonv2.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, options) {
		t.Fatalf("round trip = %#v, want %#v", got, options)
	}
}

func TestOptionsClone(t *testing.T) {
	format, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{
		Name: "answer", Schema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	options := chat.Options{
		Model:            "model",
		OutputFormat:     &format,
		FrequencyPenalty: new(0.1),
		MaxOutputTokens:  new(int64(10)),
		PresencePenalty:  new(0.2),
		ReasoningEffort:  "medium",
		Stop:             []string{"END"},
		Temperature:      new(0.3),
		TopK:             new(int64(4)),
		TopP:             new(0.9),
		Extensions:       metadata.Extensions{},
	}
	if setErr := options.Extensions.Set("test/value", "original"); setErr != nil {
		t.Fatal(setErr)
	}
	clone := options.Clone()

	clone.OutputFormat.Schema[0] = '['
	*clone.FrequencyPenalty = 1
	*clone.MaxOutputTokens = 20
	*clone.PresencePenalty = 1
	clone.Stop[0] = "MUTATED"
	*clone.Temperature = 1
	*clone.TopK = 8
	*clone.TopP = 0.5
	if setErr := clone.Extensions.Set("test/value", "changed"); setErr != nil {
		t.Fatal(setErr)
	}

	if *options.FrequencyPenalty != 0.1 ||
		options.OutputFormat.Schema[0] != '{' ||
		*options.MaxOutputTokens != 10 ||
		*options.PresencePenalty != 0.2 ||
		options.Stop[0] != "END" ||
		*options.Temperature != 0.3 ||
		*options.TopK != 4 ||
		*options.TopP != 0.9 {
		t.Fatalf("clone mutated source options: %+v", options)
	}
	value, found, err := options.Extensions.Decode[string]("test/value")
	if err != nil || !found || value != "original" {
		t.Fatalf("clone mutated source extension: %q, %v, %v", value, found, err)
	}
}

func TestOptionsResolve(t *testing.T) {
	baseFormat, err := chat.NewOutputFormat(chat.OutputFormatText)
	if err != nil {
		t.Fatal(err)
	}
	base := chat.Options{
		Model:            "base-model",
		OutputFormat:     &baseFormat,
		FrequencyPenalty: new(0.1),
		MaxOutputTokens:  new(int64(10)),
		PresencePenalty:  new(0.2),
		Stop:             []string{"BASE"},
		Temperature:      new(0.3),
		TopK:             new(int64(4)),
		TopP:             new(0.9),
	}

	got, err := base.Resolve(chat.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, base) {
		t.Fatalf("Resolve(empty) = %#v, want unchanged %#v", got, base)
	}

	override := chat.Options{
		Model:           "override-model",
		MaxOutputTokens: new(int64(20)),
		Stop:            []string{"OVERRIDE"},
		Temperature:     new(0.7),
		ReasoningEffort: "high",
	}
	overrideFormat, err := chat.NewOutputFormat(chat.OutputFormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	override.OutputFormat = &overrideFormat
	got, err = base.Resolve(override)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "override-model" || got.OutputFormat == nil || got.OutputFormat.Type != chat.OutputFormatJSON || *got.MaxOutputTokens != 20 ||
		got.ReasoningEffort != "high" ||
		got.Stop[0] != "OVERRIDE" || *got.Temperature != 0.7 {
		t.Fatalf("Resolve did not apply set fields: %#v", got)
	}
	if *got.FrequencyPenalty != 0.1 || *got.PresencePenalty != 0.2 ||
		*got.TopK != 4 || *got.TopP != 0.9 {
		t.Fatalf("Resolve dropped base fields the override left unset: %#v", got)
	}

	*got.MaxOutputTokens = 99
	got.OutputFormat.Type = chat.OutputFormatText
	got.Stop[0] = "MUTATED"
	*got.FrequencyPenalty = 99
	if *override.MaxOutputTokens != 20 || override.OutputFormat.Type != chat.OutputFormatJSON || override.Stop[0] != "OVERRIDE" {
		t.Fatalf("Resolve aliased the override: %#v", override)
	}
	if *base.FrequencyPenalty != 0.1 || base.OutputFormat.Type != chat.OutputFormatText || base.Stop[0] != "BASE" {
		t.Fatalf("Resolve aliased the base: %#v", base)
	}
}

func TestOptionsExtensions(t *testing.T) {
	var options chat.Options
	if err := options.Extensions.Set("openai/response_format", map[string]string{"type": "json_object"}); err != nil {
		t.Fatal(err)
	}
	value, ok, err := options.Extensions.Decode[map[string]string]("openai/response_format")
	if err != nil || !ok || value["type"] != "json_object" {
		t.Fatalf("Decode extension = (%v, %v, %v)", value, ok, err)
	}
	if err := options.Extensions.Set("not-namespaced", 1); err == nil {
		t.Fatalf("unscoped key error = %v", err)
	}
}

func TestOptionsValidateRejectsInvalidOverrides(t *testing.T) {
	tests := []struct {
		name    string
		options chat.Options
	}{
		{name: "model whitespace", options: chat.Options{Model: " model"}},
		{name: "invalid output format", options: chat.Options{OutputFormat: &chat.OutputFormat{}}},
		{name: "frequency low", options: chat.Options{FrequencyPenalty: new(-2.1)}},
		{name: "frequency NaN", options: chat.Options{FrequencyPenalty: new(math.NaN())}},
		{name: "max tokens zero", options: chat.Options{MaxOutputTokens: new(int64(0))}},
		{name: "presence high", options: chat.Options{PresencePenalty: new(2.1)}},
		{name: "reasoning whitespace", options: chat.Options{ReasoningEffort: " high"}},
		{name: "empty stop", options: chat.Options{Stop: []string{""}}},
		{name: "temperature high", options: chat.Options{Temperature: new(2.1)}},
		{name: "temperature infinity", options: chat.Options{Temperature: new(math.Inf(1))}},
		{name: "top k zero", options: chat.Options{TopK: new(int64(0))}},
		{name: "top p high", options: chat.Options{TopP: new(1.1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.options.Validate(); !errors.Is(err, chat.ErrInvalidOptions) {
				t.Fatalf("Validate error = %v, want ErrInvalidOptions", err)
			}
			if _, err := jsonv2.Marshal(tt.options); !errors.Is(err, chat.ErrInvalidOptions) {
				t.Fatalf("Marshal error = %v, want ErrInvalidOptions", err)
			}
		})
	}
}

func TestOptionsUnmarshalIsAtomic(t *testing.T) {
	got := chat.Options{Model: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"temperature":3}`), &got); !errors.Is(err, chat.ErrInvalidOptions) {
		t.Fatalf("Unmarshal error = %v, want ErrInvalidOptions", err)
	}
	if got.Model != "keep" {
		t.Fatalf("failed Unmarshal mutated receiver: %+v", got)
	}
}

func TestOptionsNilUnmarshalReceiver(t *testing.T) {
	var options *chat.Options
	if err := options.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, chat.ErrInvalidOptions) {
		t.Fatalf("UnmarshalJSON error = %v, want ErrInvalidOptions", err)
	}
}

func TestToolChoiceValidateAndClone(t *testing.T) {
	valid := []chat.ToolChoice{
		{Mode: chat.ToolChoiceAuto},
		{Mode: chat.ToolChoiceNone},
		{Mode: chat.ToolChoiceRequired, Parallelism: chat.ToolParallelismAllow},
		{Mode: chat.ToolChoiceNamed, Name: "weather", Parallelism: chat.ToolParallelismSingle},
	}
	for _, choice := range valid {
		if err := choice.Validate(); err != nil {
			t.Errorf("Validate(%+v): %v", choice, err)
		}
	}

	invalid := []chat.ToolChoice{
		{},
		{Mode: "future"},
		{Mode: chat.ToolChoiceAuto, Name: "weather"},
		{Mode: chat.ToolChoiceNamed},
		{Mode: chat.ToolChoiceNamed, Name: "not valid"},
		{Mode: chat.ToolChoiceNone, Parallelism: chat.ToolParallelismSingle},
		{Mode: chat.ToolChoiceAuto, Parallelism: "future"},
	}
	for _, choice := range invalid {
		if err := choice.Validate(); !errors.Is(err, chat.ErrInvalidToolChoice) {
			t.Errorf("Validate(%+v) error = %v, want ErrInvalidToolChoice", choice, err)
		}
	}
}
