package speech_test

import (
	"context"
	"errors"
	"iter"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/speech"
)

func TestModelAndStreamerFunc(t *testing.T) {
	want := errors.New("boom")
	model := speech.ModelFunc(func(_ context.Context, request *speech.Request) (*speech.Response, error) {
		if request.Text != "hello" {
			t.Fatalf("text = %q", request.Text)
		}
		return nil, want
	})
	request, _ := speech.NewRequest("hello")
	if _, err := model.Call(t.Context(), request); !errors.Is(err, want) {
		t.Fatalf("Call error = %v, want %v", err, want)
	}

	streamer := speech.StreamerFunc(func(context.Context, *speech.Request) iter.Seq2[*speech.Response, error] {
		return func(yield func(*speech.Response, error) bool) {
			yield(nil, want)
		}
	})
	for _, err := range streamer.Stream(t.Context(), request) {
		if !errors.Is(err, want) {
			t.Fatalf("Stream error = %v, want %v", err, want)
		}
		return
	}
	t.Fatal("Stream yielded nothing")
}

func TestOptionsAndRequestValidation(t *testing.T) {
	if err := (speech.Options{Model: " model "}).Validate(); err == nil {
		t.Fatal("Options accepted model with surrounding whitespace")
	}
	if _, err := speech.NewRequest(""); err == nil {
		t.Fatal("NewRequest accepted empty text")
	}
	if resolved, err := (speech.Options{}).Resolve(speech.Options{}); err != nil || resolved.Model != "" || resolved.Speed != 0 || !resolved.Extensions.IsZero() {
		t.Fatalf("zero Options.Resolve(empty) = %#v, %v", resolved, err)
	}
	if err := (*speech.Request)(nil).Validate(); err == nil {
		t.Fatal("Validate accepted nil request")
	}
	invalid := &speech.Request{Text: "text"}
	invalid.Options = speech.Options{Model: " model "}
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate accepted model with surrounding whitespace")
	}
	for _, tc := range []struct {
		name  string
		speed float64
	}{
		{name: "negative", speed: -1},
		{name: "nan", speed: math.NaN()},
		{name: "positive infinity", speed: math.Inf(1)},
		{name: "negative infinity", speed: math.Inf(-1)},
	} {
		t.Run(tc.name+" speed", func(t *testing.T) {
			invalid.Options = speech.Options{Speed: tc.speed}
			if err := invalid.Validate(); err == nil {
				t.Fatalf("Validate accepted speed %v", tc.speed)
			}
		})
	}
	options := new(speech.Options)
	if err := options.Extensions.Set("provider/value", func() {}); err == nil || !options.Extensions.IsZero() {
		t.Fatalf("failed SetExtension mutated options: %#v, %v", options.Extensions, err)
	}
	if _, err := (speech.Options{Model: "base", Speed: math.NaN()}).Resolve(speech.Options{}); err == nil {
		t.Fatal("Resolve accepted invalid base options")
	}
}

func TestResponseValidation(t *testing.T) {
	if _, err := speech.NewOutput("audio/mpeg", nil, nil); !errors.Is(err, speech.ErrInvalidResponse) {
		t.Fatalf("NewOutput empty audio error = %v", err)
	}
	for _, mimeType := range []string{"", "audio", "not a type", "text/plain", "application/octet-stream"} {
		if _, err := speech.NewOutput(mimeType, []byte("audio"), nil); !errors.Is(err, speech.ErrInvalidResponse) {
			t.Fatalf("NewOutput(%q) error = %v, want ErrInvalidResponse", mimeType, err)
		}
	}
	output, _ := speech.NewOutput("audio/mpeg", []byte("audio"), nil)
	if _, err := speech.NewResponse(output, &speech.ResponseMetadata{}); err != nil {
		t.Fatal(err)
	}
}

func TestOptionsResolveAndCopies(t *testing.T) {
	base := speech.Options{Model: "base", Voice: "base-voice", Extensions: mustExtensions(t, map[string]any{"provider/base": true})}
	resolved, err := base.Resolve(speech.Options{
		Model: "override", Voice: "alloy", OutputFormat: "mp3", Speed: 1.25,
		Extensions: mustExtensions(t, map[string]any{"provider/override": true}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "override" || resolved.Voice != "alloy" || resolved.OutputFormat != "mp3" ||
		resolved.Speed != 1.25 || !mustDecode[bool](t, resolved.Extensions, "provider/base") ||
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

func TestResponseAndRequestErrorBoundaries(t *testing.T) {
	if _, err := speech.NewRequest(""); err == nil {
		t.Fatal("NewRequest accepted empty text")
	}
	audio := []byte("audio")
	output, _ := speech.NewOutput("audio/mpeg", audio, nil)
	audio[0] = 'X'
	if string(output.Audio) != "audio" {
		t.Fatal("NewOutput aliases caller audio")
	}
	if _, err := speech.NewResponse(nil, &speech.ResponseMetadata{}); err == nil {
		t.Fatal("NewResponse accepted nil output")
	}
	if _, err := speech.NewResponse(output, nil); err != nil {
		t.Fatalf("NewResponse rejected optional metadata: %v", err)
	}
}
