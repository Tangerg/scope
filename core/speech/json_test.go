package speech_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/speech"
)

func TestJSONBoundaries(t *testing.T) {
	if err := (speech.Options{Model: " model "}).Validate(); !errors.Is(err, speech.ErrInvalidOptions) {
		t.Fatalf("NewOptions error = %v", err)
	}
	if _, err := speech.NewRequest(""); !errors.Is(err, speech.ErrInvalidRequest) {
		t.Fatalf("NewRequest error = %v", err)
	}
	if _, err := speech.NewResponse(nil, &speech.ResponseMetadata{}); !errors.Is(err, speech.ErrInvalidResponse) {
		t.Fatalf("NewResponse error = %v", err)
	}
	var extensionOptions speech.Options
	if err := extensionOptions.Extensions.Set("invalid", true); err == nil {
		t.Fatalf("SetExtension error = %v", err)
	}

	if _, err := jsonv2.Marshal(speech.Options{Model: " invalid "}); !errors.Is(err, speech.ErrInvalidOptions) {
		t.Fatalf("Marshal Options error = %v", err)
	}
	if _, err := jsonv2.Marshal(speech.Request{}); !errors.Is(err, speech.ErrInvalidRequest) {
		t.Fatalf("Marshal Request error = %v", err)
	}
	if _, err := jsonv2.Marshal(speech.Response{}); !errors.Is(err, speech.ErrInvalidResponse) {
		t.Fatalf("Marshal Response error = %v", err)
	}

	options := speech.Options{Model: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"model":" invalid "}`), &options); !errors.Is(err, speech.ErrInvalidOptions) {
		t.Fatalf("Unmarshal Options error = %v", err)
	}
	if options.Model != "keep" {
		t.Fatalf("failed Options decode mutated receiver: %#v", options)
	}

	request := speech.Request{Text: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"text":""}`), &request); !errors.Is(err, speech.ErrInvalidRequest) {
		t.Fatalf("Unmarshal Request error = %v", err)
	}
	if request.Text != "keep" {
		t.Fatalf("failed Request decode mutated receiver: %#v", request)
	}

	output, err := speech.NewOutput([]byte("audio"), nil)
	if err != nil {
		t.Fatal(err)
	}
	response := speech.Response{
		Output:   output,
		Metadata: &speech.ResponseMetadata{},
	}
	if err := jsonv2.Unmarshal([]byte(`{"output":null,"metadata":{}}`), &response); !errors.Is(err, speech.ErrInvalidResponse) {
		t.Fatalf("Unmarshal Response error = %v", err)
	}
	if response.Output != output {
		t.Fatalf("failed Response decode mutated receiver: %#v", response)
	}
}
