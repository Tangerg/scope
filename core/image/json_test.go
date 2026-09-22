package image_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
)

func TestJSONBoundaries(t *testing.T) {
	if err := (image.Options{Model: " model "}).Validate(); !errors.Is(err, image.ErrInvalidOptions) {
		t.Fatalf("NewOptions error = %v", err)
	}
	if _, err := image.NewRequest(""); !errors.Is(err, image.ErrInvalidRequest) {
		t.Fatalf("NewRequest error = %v", err)
	}
	if _, err := image.NewResponse(nil, &image.ResponseMetadata{}); !errors.Is(err, image.ErrInvalidResponse) {
		t.Fatalf("NewResponse error = %v", err)
	}
	var extensionOptions image.Options
	if err := extensionOptions.Extensions.Set("invalid", true); err == nil {
		t.Fatalf("SetExtension error = %v", err)
	}

	if _, err := jsonv2.Marshal(image.Options{Model: " invalid "}); !errors.Is(err, image.ErrInvalidOptions) {
		t.Fatalf("Marshal Options error = %v", err)
	}
	if _, err := jsonv2.Marshal(image.Request{}); !errors.Is(err, image.ErrInvalidRequest) {
		t.Fatalf("Marshal Request error = %v", err)
	}
	if _, err := jsonv2.Marshal(image.Response{}); !errors.Is(err, image.ErrInvalidResponse) {
		t.Fatalf("Marshal Response error = %v", err)
	}

	options := image.Options{Model: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"model":" invalid "}`), &options); !errors.Is(err, image.ErrInvalidOptions) {
		t.Fatalf("Unmarshal Options error = %v", err)
	}
	if options.Model != "keep" {
		t.Fatalf("failed Options decode mutated receiver: %#v", options)
	}

	request := image.Request{Prompt: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"prompt":""}`), &request); !errors.Is(err, image.ErrInvalidRequest) {
		t.Fatalf("Unmarshal Request error = %v", err)
	}
	if request.Prompt != "keep" {
		t.Fatalf("failed Request decode mutated receiver: %#v", request)
	}

	generated, err := media.NewURI("image/png", "https://example.com/image.png")
	if err != nil {
		t.Fatal(err)
	}
	output, err := image.NewOutput(generated, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := image.Response{
		Outputs:  []*image.Output{output},
		Metadata: &image.ResponseMetadata{},
	}
	if err := jsonv2.Unmarshal([]byte(`{"outputs":[],"metadata":{}}`), &response); !errors.Is(err, image.ErrInvalidResponse) {
		t.Fatalf("Unmarshal Response error = %v", err)
	}
	if response.First() != output {
		t.Fatalf("failed Response decode mutated receiver: %#v", response)
	}
}
