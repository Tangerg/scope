package image_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

func TestModelFunc(t *testing.T) {
	want := errors.New("boom")
	model := image.ModelFunc(func(_ context.Context, request *image.Request) (*image.Response, error) {
		if request.Prompt != "a duck" {
			t.Fatalf("prompt = %q", request.Prompt)
		}
		return nil, want
	})
	request, _ := image.NewRequest("a duck")
	if _, err := model.Call(t.Context(), request); !errors.Is(err, want) {
		t.Fatalf("Call error = %v, want %v", err, want)
	}
}

func TestOptionsAndRequestValidation(t *testing.T) {
	if err := (image.Options{Model: " model "}).Validate(); err == nil {
		t.Fatal("Options accepted model with surrounding whitespace")
	}
	if _, err := image.NewRequest(""); err == nil {
		t.Fatal("NewRequest accepted empty prompt")
	}
	if resolved, err := (image.Options{}).Resolve(image.Options{}); err != nil || resolved.Model != "" || resolved.Width != nil || !resolved.Extensions.IsZero() {
		t.Fatalf("zero Options.Resolve(empty) = %#v, %v", resolved, err)
	}
	if err := (*image.Request)(nil).Validate(); err == nil {
		t.Fatal("Validate accepted nil request")
	}
	invalid := &image.Request{Prompt: "scope"}
	invalid.Options = image.Options{Model: " model "}
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate accepted model with surrounding whitespace")
	}
	width := int64(0)
	invalid.Options = image.Options{Width: &width}
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate accepted non-positive width")
	}
	seed := int64(-1)
	invalid.Options = image.Options{Seed: &seed}
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate accepted a negative seed")
	}
	invalid.Options = image.Options{OutputFormat: "IMAGE/PNG"}
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate accepted a non-canonical output MIME type")
	}
	options := new(image.Options)
	if err := options.Extensions.Set("provider/value", func() {}); err == nil || !options.Extensions.IsZero() {
		t.Fatalf("failed SetExtension mutated options: %#v, %v", options.Extensions, err)
	}
	base := image.Options{Model: "image-model"}
	err := base.Validate()
	if err != nil {
		t.Fatal(err)
	}
	base.OutputFormat = "IMAGE/PNG"
	if _, err := base.Resolve(image.Options{}); err == nil {
		t.Fatal("Resolve accepted a non-canonical output MIME type")
	}
	for _, invalid := range []string{"text/plain", "image", "image/png;charset=utf-8"} {
		base.OutputFormat = invalid
		if _, err := base.Resolve(image.Options{}); err == nil {
			t.Errorf("Resolve accepted invalid OutputFormat %q", invalid)
		}
	}
	if _, err := (image.Options{Model: " model "}).Resolve(image.Options{}); err == nil {
		t.Fatal("Resolve accepted invalid base options")
	}
}

func TestResponseValidation(t *testing.T) {
	generated, err := media.NewURI("image/png", "https://example.com/image.png")
	if err != nil {
		t.Fatal(err)
	}
	output, err := image.NewOutput(generated, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := image.NewResponse([]*image.Output{output}, &image.ResponseMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	if response.First() != output {
		t.Fatal("First did not return the first output")
	}
	if (&image.Response{}).First() != nil || (*image.Response)(nil).First() != nil {
		t.Fatal("empty response returned a output")
	}
}

func TestOptionsResolveAndCopies(t *testing.T) {
	width, height, seed := int64(512), int64(768), int64(7)
	base := image.Options{Model: "base", Width: &width, Extensions: mustExtensions(t, map[string]any{"provider/base": true})}
	override := image.Options{
		Model: "override", NegativePrompt: "text", Width: &width, Height: &height,
		Seed: &seed, OutputFormat: "image/png",
		Extensions: mustExtensions(t, map[string]any{"provider/override": true}),
	}
	resolved, err := base.Resolve(override)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "override" || resolved.NegativePrompt != "text" || resolved.Height == nil ||
		resolved.Seed == nil || resolved.OutputFormat != "image/png" {
		t.Fatalf("Resolve = %#v", resolved)
	}
	if !mustDecodeExtension[bool](t, resolved.Extensions, "provider/base") ||
		!mustDecodeExtension[bool](t, resolved.Extensions, "provider/override") {
		t.Fatalf("resolved Extensions = %#v", resolved.Extensions)
	}
	*resolved.Height = 1024
	*resolved.Seed = 9
	if height != 768 || seed != 7 {
		t.Fatal("Resolve aliases override pointer state")
	}
	clone := resolved.Clone()
	*clone.Width = 1024
	if err := clone.Extensions.Set("provider/base", false); err != nil {
		t.Fatal(err)
	}
	if *resolved.Width != 512 || !mustDecodeExtension[bool](t, resolved.Extensions, "provider/base") {
		t.Fatal("Options.Clone aliases source state")
	}
}

func TestResponseMetadataAndErrors(t *testing.T) {
	resultMetadata := mustMetadata(t, map[string]any{"revised_prompt": "scope"})
	responseMetadata := &image.ResponseMetadata{Extra: mustMetadata(t, map[string]any{"region": "local"})}

	generated, _ := media.NewURI("image/png", "https://example.com/image.png")
	if _, err := image.NewOutput(nil, resultMetadata); err == nil {
		t.Fatal("NewOutput accepted nil media")
	}
	audio, _ := media.NewBytes("audio/mpeg", []byte("audio"))
	if _, err := image.NewOutput(audio, resultMetadata); err == nil {
		t.Fatal("NewOutput accepted non-image media")
	}
	output, _ := image.NewOutput(generated, resultMetadata)
	if _, err := image.NewResponse(nil, responseMetadata); err == nil {
		t.Fatal("NewResponse accepted no outputs")
	}
	if _, err := image.NewResponse([]*image.Output{nil}, responseMetadata); err == nil {
		t.Fatal("NewResponse accepted nil output")
	}
	invalidMetadata := &image.ResponseMetadata{Extra: metadata.Map{"": nil}}
	if _, err := image.NewResponse([]*image.Output{output}, invalidMetadata); !errors.Is(err, image.ErrInvalidResponse) {
		t.Fatalf("NewResponse metadata error = %v, want %v", err, image.ErrInvalidResponse)
	}
	if _, err := image.NewResponse([]*image.Output{output}, nil); err != nil {
		t.Fatalf("NewResponse rejected optional metadata: %v", err)
	}
}

func mustMetadata(t *testing.T, values map[string]any) metadata.Map {
	t.Helper()
	output, err := metadata.FromValues(values)
	if err != nil {
		t.Fatal(err)
	}
	return output
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

func mustDecodeExtension[T any](t *testing.T, values metadata.Extensions, key string) T {
	t.Helper()
	value, ok, err := values.Decode[T](key)
	if err != nil || !ok {
		t.Fatalf("Extensions.Decode(%q) = %#v, %t, %v", key, value, ok, err)
	}
	return value
}

func TestOutputSnapshotsMedia(t *testing.T) {
	source, err := media.NewBytes("image/png", []byte{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	source.Metadata = metadata.Map{}
	if err = source.Metadata.Set("key", "original"); err != nil {
		t.Fatal(err)
	}
	output, err := image.NewOutput(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	source.Source.Bytes[0] = 9
	source.MIME = "image/jpeg"
	if err = source.Metadata.Set("key", "changed"); err != nil {
		t.Fatal(err)
	}
	if output.Media.Source.Bytes[0] != 1 || output.Media.MIME != "image/png" || string(output.Media.Metadata["key"]) != `"original"` {
		t.Fatalf("output shares source: %#v", output.Media)
	}
	output.Media.Source.Bytes[1] = 7
	if source.Source.Bytes[1] != 2 {
		t.Fatal("source shares output buffer")
	}
}
