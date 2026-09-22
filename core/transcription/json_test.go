package transcription_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/transcription"
)

func TestJSONBoundaries(t *testing.T) {
	if err := (transcription.Options{Model: " model "}).Validate(); !errors.Is(err, transcription.ErrInvalidOptions) {
		t.Fatalf("NewOptions error = %v", err)
	}
	if _, err := transcription.NewRequest(nil); !errors.Is(err, transcription.ErrInvalidRequest) {
		t.Fatalf("NewRequest error = %v", err)
	}
	if _, err := transcription.NewResponse(nil, &transcription.ResponseMetadata{}); !errors.Is(err, transcription.ErrInvalidResponse) {
		t.Fatalf("NewResponse error = %v", err)
	}
	var extensionOptions transcription.Options
	if err := extensionOptions.Extensions.Set("invalid", true); err == nil {
		t.Fatalf("SetExtension error = %v", err)
	}

	if _, err := jsonv2.Marshal(transcription.Options{Model: " invalid "}); !errors.Is(err, transcription.ErrInvalidOptions) {
		t.Fatalf("Marshal Options error = %v", err)
	}
	if _, err := jsonv2.Marshal(transcription.Request{}); !errors.Is(err, transcription.ErrInvalidRequest) {
		t.Fatalf("Marshal Request error = %v", err)
	}
	if _, err := jsonv2.Marshal(transcription.Response{}); !errors.Is(err, transcription.ErrInvalidResponse) {
		t.Fatalf("Marshal Response error = %v", err)
	}

	options := transcription.Options{Model: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"model":" invalid "}`), &options); !errors.Is(err, transcription.ErrInvalidOptions) {
		t.Fatalf("Unmarshal Options error = %v", err)
	}
	if options.Model != "keep" {
		t.Fatalf("failed Options decode mutated receiver: %#v", options)
	}

	audio, err := media.NewBytes("audio/mpeg", []byte("audio"))
	if err != nil {
		t.Fatal(err)
	}
	request := transcription.Request{Audio: audio}
	if unmarshalErr := jsonv2.Unmarshal([]byte(`{"audio":null}`), &request); !errors.Is(unmarshalErr, transcription.ErrInvalidRequest) {
		t.Fatalf("Unmarshal Request error = %v", unmarshalErr)
	}
	if request.Audio != audio {
		t.Fatalf("failed Request decode mutated receiver: %#v", request)
	}

	output, err := transcription.NewOutput("keep", nil)
	if err != nil {
		t.Fatal(err)
	}
	response := transcription.Response{
		Output:   output,
		Metadata: &transcription.ResponseMetadata{},
	}
	if err := jsonv2.Unmarshal([]byte(`{"output":null,"metadata":{}}`), &response); !errors.Is(err, transcription.ErrInvalidResponse) {
		t.Fatalf("Unmarshal Response error = %v", err)
	}
	if response.Output != output {
		t.Fatalf("failed Response decode mutated receiver: %#v", response)
	}
}
