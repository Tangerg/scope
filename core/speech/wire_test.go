package speech_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/speech"
)

func TestOptionsRoundTrip(t *testing.T) {
	options := speech.Options{
		Model:        "tts-model",
		Voice:        "alloy",
		OutputFormat: "mp3",
		Speed:        1.25,
		Extensions:   mustExtensions(t, map[string]any{"provider/sample_rate": 24000}),
	}
	encoded, err := jsonv2.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}

	var decoded speech.Options
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Model != options.Model || decoded.Voice != options.Voice ||
		decoded.OutputFormat != options.OutputFormat || decoded.Speed != options.Speed ||
		!decoded.Extensions.Equal(options.Extensions) {
		t.Fatalf("Options round trip = %#v, want %#v", decoded, options)
	}
}

func TestRequestRoundTrip(t *testing.T) {
	request, err := speech.NewRequest("say this")
	if err != nil {
		t.Fatal(err)
	}
	request.Options = speech.Options{Model: "tts-model", Voice: "alloy"}

	encoded, err := jsonv2.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	var decoded speech.Request
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Text != "say this" || decoded.Options.Voice != "alloy" {
		t.Fatalf("Request round trip = %#v", decoded)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	output, err := speech.NewOutput([]byte("audio"), metadata.Map{"provider/chunk": json.RawMessage(`0`)})
	if err != nil {
		t.Fatal(err)
	}
	response, err := speech.NewResponse(output, &speech.ResponseMetadata{
		Model:     "tts-model",
		CreatedAt: time.Unix(1700000000, 0).UTC(),
		Extra:     metadata.Map{"provider/region": json.RawMessage(`"eu"`)},
	})
	if err != nil {
		t.Fatal(err)
	}

	encoded, err := jsonv2.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}

	var decoded speech.Response
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Output == nil || string(decoded.Output.Audio) != "audio" {
		t.Fatalf("Response round trip lost audio: %#v", decoded.Output)
	}
	if decoded.Metadata == nil || decoded.Metadata.Model != "tts-model" || !decoded.Metadata.CreatedAt.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("Response round trip lost metadata: %#v", decoded.Metadata)
	}
	if !decoded.Metadata.Extra.Equal(response.Metadata.Extra) {
		t.Fatalf("Response round trip lost extra metadata: %#v", decoded.Metadata.Extra)
	}
}

func TestOutputAndResponseMetadataRoundTrip(t *testing.T) {
	output, err := speech.NewOutput([]byte("chunk"), nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	var decodedOutput speech.Output
	if err = jsonv2.Unmarshal(encoded, &decodedOutput); err != nil {
		t.Fatal(err)
	}
	if string(decodedOutput.Audio) != "chunk" {
		t.Fatalf("Output round trip = %#v", decodedOutput)
	}

	responseMetadata := speech.ResponseMetadata{Model: "tts-model", CreatedAt: time.Unix(7, 0).UTC()}
	encoded, err = jsonv2.Marshal(responseMetadata)
	if err != nil {
		t.Fatal(err)
	}
	var decodedMetadata speech.ResponseMetadata
	if err := jsonv2.Unmarshal(encoded, &decodedMetadata); err != nil {
		t.Fatal(err)
	}
	if decodedMetadata.Model != responseMetadata.Model || decodedMetadata.CreatedAt != responseMetadata.CreatedAt {
		t.Fatalf("ResponseMetadata round trip = %#v, want %#v", decodedMetadata, responseMetadata)
	}
}

// TestMalformedJSONIsRejectedPerType keeps every protocol decoder reporting its
// own package sentinel, so a transport-level syntax error stays classifiable by
// the value it was decoding into.
func TestMalformedJSONIsRejectedPerType(t *testing.T) {
	malformed := []byte(`{`)
	cases := map[string]struct {
		target json.Unmarshaler
		want   error
	}{
		"options":           {target: new(speech.Options), want: speech.ErrInvalidOptions},
		"request":           {target: new(speech.Request), want: speech.ErrInvalidRequest},
		"output":            {target: new(speech.Output), want: speech.ErrInvalidResponse},
		"response metadata": {target: new(speech.ResponseMetadata), want: speech.ErrInvalidResponse},
		"response":          {target: new(speech.Response), want: speech.ErrInvalidResponse},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if err := testCase.target.UnmarshalJSON(malformed); !errors.Is(err, testCase.want) {
				t.Fatalf("UnmarshalJSON error = %v, want %v", err, testCase.want)
			}
		})
	}
}

// TestNilReceiversAreRejected proves the decoders refuse to write through a nil
// receiver instead of panicking inside encoding/json.
func TestNilReceiversAreRejected(t *testing.T) {
	cases := map[string]struct {
		target json.Unmarshaler
		want   error
	}{
		"options":           {target: (*speech.Options)(nil), want: speech.ErrInvalidOptions},
		"request":           {target: (*speech.Request)(nil), want: speech.ErrInvalidRequest},
		"output":            {target: (*speech.Output)(nil), want: speech.ErrInvalidResponse},
		"response metadata": {target: (*speech.ResponseMetadata)(nil), want: speech.ErrInvalidResponse},
		"response":          {target: (*speech.Response)(nil), want: speech.ErrInvalidResponse},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if err := testCase.target.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, testCase.want) {
				t.Fatalf("UnmarshalJSON error = %v, want %v", err, testCase.want)
			}
		})
	}
}

// TestDecodedValuesAreValidatedBeforeAssignment covers the branch where the
// payload is syntactically valid JSON but violates the protocol: the receiver
// must keep its previous value.
func TestDecodedValuesAreValidatedBeforeAssignment(t *testing.T) {
	options := speech.Options{Model: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"model":" padded "}`), &options); !errors.Is(err, speech.ErrInvalidOptions) {
		t.Fatalf("Options decode error = %v", err)
	}
	if options.Model != "keep" {
		t.Fatalf("failed Options decode mutated receiver: %#v", options)
	}

	request := speech.Request{Text: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"text":""}`), &request); !errors.Is(err, speech.ErrInvalidRequest) {
		t.Fatalf("Request decode error = %v", err)
	}
	if request.Text != "keep" {
		t.Fatalf("failed Request decode mutated receiver: %#v", request)
	}

	output := speech.Output{Audio: []byte("keep")}
	if err := jsonv2.Unmarshal([]byte(`{"audio":""}`), &output); !errors.Is(err, speech.ErrInvalidResponse) {
		t.Fatalf("Output decode error = %v", err)
	}
	if string(output.Audio) != "keep" {
		t.Fatalf("failed Output decode mutated receiver: %#v", output)
	}

	responseMetadata := speech.ResponseMetadata{Model: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"model":" padded "}`), &responseMetadata); !errors.Is(err, speech.ErrInvalidResponse) {
		t.Fatalf("ResponseMetadata decode error = %v", err)
	}
	if responseMetadata.Model != "keep" {
		t.Fatalf("failed ResponseMetadata decode mutated receiver: %#v", responseMetadata)
	}

	kept := speech.Output{Audio: []byte("keep")}
	response := speech.Response{Output: &kept}
	if err := jsonv2.Unmarshal([]byte(`{"output":null}`), &response); !errors.Is(err, speech.ErrInvalidResponse) {
		t.Fatalf("Response decode error = %v", err)
	}
	if response.Output == nil {
		t.Fatalf("failed Response decode mutated receiver: %#v", response)
	}
}

func TestInvalidValuesFailToMarshal(t *testing.T) {
	brokenExtra := metadata.Map{"provider/broken": json.RawMessage(`{`)}
	cases := map[string]struct {
		value any
		want  error
	}{
		"padded options model":    {value: speech.Options{Model: " padded "}, want: speech.ErrInvalidOptions},
		"empty request text":      {value: speech.Request{}, want: speech.ErrInvalidRequest},
		"empty output audio":      {value: speech.Output{}, want: speech.ErrInvalidResponse},
		"invalid output metadata": {value: speech.Output{Audio: []byte("a"), Metadata: brokenExtra}, want: speech.ErrInvalidResponse},
		"invalid extra":           {value: speech.ResponseMetadata{Extra: brokenExtra}, want: speech.ErrInvalidResponse},
		"missing response output": {value: speech.Response{}, want: speech.ErrInvalidResponse},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := jsonv2.Marshal(testCase.value); !errors.Is(err, testCase.want) {
				t.Fatalf("Marshal error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestNilResponseValidateIsRejected(t *testing.T) {
	if err := (*speech.Response)(nil).Validate(); !errors.Is(err, speech.ErrInvalidResponse) {
		t.Fatalf("Validate error = %v", err)
	}
	if err := (*speech.Output)(nil).Validate(); !errors.Is(err, speech.ErrInvalidResponse) {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestOptionsResolveRejectsInvalidOverride(t *testing.T) {
	if _, err := (speech.Options{}).Resolve(speech.Options{Model: " padded "}); !errors.Is(err, speech.ErrInvalidOptions) {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestNewOutputClonesCallerMetadata(t *testing.T) {
	callerMetadata := metadata.Map{"provider/chunk": json.RawMessage(`0`)}
	output, err := speech.NewOutput([]byte("audio"), callerMetadata)
	if err != nil {
		t.Fatal(err)
	}
	callerMetadata["provider/chunk"] = json.RawMessage(`1`)
	if string(output.Metadata["provider/chunk"]) != "0" {
		t.Fatal("NewOutput aliases the caller metadata")
	}
}
