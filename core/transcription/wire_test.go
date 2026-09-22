package transcription_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/transcription"
)

func audioFixture(t *testing.T) *media.Media {
	t.Helper()
	audio, err := media.NewBytes("audio/wav", []byte("riff"))
	if err != nil {
		t.Fatal(err)
	}
	return audio
}

func TestOptionsRoundTrip(t *testing.T) {
	options := transcription.Options{
		Model:      "whisper-1",
		Language:   "en",
		Extensions: mustExtensions(t, map[string]any{"provider/temperature": 0.2}),
	}
	encoded, err := jsonv2.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}

	var decoded transcription.Options
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Model != options.Model || decoded.Language != options.Language ||
		!decoded.Extensions.Equal(options.Extensions) {
		t.Fatalf("Options round trip = %#v, want %#v", decoded, options)
	}
}

func TestRequestRoundTrip(t *testing.T) {
	request, err := transcription.NewRequest(audioFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	request.Options = transcription.Options{Model: "whisper-1", Language: "en"}

	encoded, err := jsonv2.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	var decoded transcription.Request
	if err = jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Audio == nil || decoded.Audio.MIME != "audio/wav" {
		t.Fatalf("Request round trip lost audio: %#v", decoded.Audio)
	}
	audio, err := decoded.Audio.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(audio) != "riff" {
		t.Fatalf("Request round trip lost audio bytes: %q", audio)
	}
	if decoded.Options.Language != "en" {
		t.Fatalf("Request round trip lost options: %#v", decoded.Options)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	output, err := transcription.NewOutput("hello world", metadata.Map{
		"provider/segments": json.RawMessage(`[{"start":0,"end":1}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := transcription.NewResponse(output, &transcription.ResponseMetadata{
		Model:     "whisper-1",
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

	var decoded transcription.Response
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Output == nil || decoded.Output.Text != "hello world" {
		t.Fatalf("Response round trip lost text: %#v", decoded.Output)
	}
	if !decoded.Output.Metadata.Equal(output.Metadata) {
		t.Fatalf("Response round trip lost segment metadata: %#v", decoded.Output.Metadata)
	}
	if decoded.Metadata == nil || decoded.Metadata.Model != "whisper-1" || !decoded.Metadata.CreatedAt.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("Response round trip lost metadata: %#v", decoded.Metadata)
	}
}

func TestOutputAndResponseMetadataRoundTrip(t *testing.T) {
	output, err := transcription.NewOutput("", nil)
	if err != nil {
		t.Fatalf("NewOutput rejected an empty silence segment: %v", err)
	}
	encoded, err := jsonv2.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	var decodedOutput transcription.Output
	if err = jsonv2.Unmarshal(encoded, &decodedOutput); err != nil {
		t.Fatal(err)
	}
	if decodedOutput.Text != "" {
		t.Fatalf("Output round trip = %#v", decodedOutput)
	}

	responseMetadata := transcription.ResponseMetadata{Model: "whisper-1", CreatedAt: time.Unix(7, 0).UTC()}
	encoded, err = jsonv2.Marshal(responseMetadata)
	if err != nil {
		t.Fatal(err)
	}
	var decodedMetadata transcription.ResponseMetadata
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
		"options":           {target: new(transcription.Options), want: transcription.ErrInvalidOptions},
		"request":           {target: new(transcription.Request), want: transcription.ErrInvalidRequest},
		"output":            {target: new(transcription.Output), want: transcription.ErrInvalidResponse},
		"response metadata": {target: new(transcription.ResponseMetadata), want: transcription.ErrInvalidResponse},
		"response":          {target: new(transcription.Response), want: transcription.ErrInvalidResponse},
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
		"options":           {target: (*transcription.Options)(nil), want: transcription.ErrInvalidOptions},
		"request":           {target: (*transcription.Request)(nil), want: transcription.ErrInvalidRequest},
		"output":            {target: (*transcription.Output)(nil), want: transcription.ErrInvalidResponse},
		"response metadata": {target: (*transcription.ResponseMetadata)(nil), want: transcription.ErrInvalidResponse},
		"response":          {target: (*transcription.Response)(nil), want: transcription.ErrInvalidResponse},
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
	options := transcription.Options{Model: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"model":" padded "}`), &options); !errors.Is(err, transcription.ErrInvalidOptions) {
		t.Fatalf("Options decode error = %v", err)
	}
	if options.Model != "keep" {
		t.Fatalf("failed Options decode mutated receiver: %#v", options)
	}

	request := transcription.Request{Audio: audioFixture(t)}
	if err := jsonv2.Unmarshal([]byte(`{"audio":null}`), &request); !errors.Is(err, transcription.ErrInvalidRequest) {
		t.Fatalf("Request decode error = %v", err)
	}
	if request.Audio == nil {
		t.Fatalf("failed Request decode mutated receiver: %#v", request)
	}

	output := transcription.Output{Text: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"metadata":{"":1}}`), &output); !errors.Is(err, transcription.ErrInvalidResponse) {
		t.Fatalf("Output decode error = %v", err)
	}
	if output.Text != "keep" {
		t.Fatalf("failed Output decode mutated receiver: %#v", output)
	}

	responseMetadata := transcription.ResponseMetadata{Model: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"created_at":"not-a-time"}`), &responseMetadata); !errors.Is(err, transcription.ErrInvalidResponse) {
		t.Fatalf("ResponseMetadata decode error = %v", err)
	}
	if responseMetadata.Model != "keep" {
		t.Fatalf("failed ResponseMetadata decode mutated receiver: %#v", responseMetadata)
	}

	kept := transcription.Output{Text: "keep"}
	response := transcription.Response{Output: &kept}
	if err := jsonv2.Unmarshal([]byte(`{"output":null}`), &response); !errors.Is(err, transcription.ErrInvalidResponse) {
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
		"padded options model":    {value: transcription.Options{Model: " padded "}, want: transcription.ErrInvalidOptions},
		"padded language":         {value: transcription.Options{Language: " en "}, want: transcription.ErrInvalidOptions},
		"missing audio":           {value: transcription.Request{}, want: transcription.ErrInvalidRequest},
		"invalid output metadata": {value: transcription.Output{Metadata: brokenExtra}, want: transcription.ErrInvalidResponse},
		"padded metadata model":   {value: transcription.ResponseMetadata{Model: " padded "}, want: transcription.ErrInvalidResponse},
		"invalid extra":           {value: transcription.ResponseMetadata{Extra: brokenExtra}, want: transcription.ErrInvalidResponse},
		"missing response output": {value: transcription.Response{}, want: transcription.ErrInvalidResponse},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := jsonv2.Marshal(testCase.value); !errors.Is(err, testCase.want) {
				t.Fatalf("Marshal error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestNilValidateIsRejected(t *testing.T) {
	if err := (*transcription.Request)(nil).Validate(); !errors.Is(err, transcription.ErrInvalidRequest) {
		t.Fatalf("Validate error = %v", err)
	}
	if err := (*transcription.Response)(nil).Validate(); !errors.Is(err, transcription.ErrInvalidResponse) {
		t.Fatalf("Validate error = %v", err)
	}
	if err := (*transcription.Output)(nil).Validate(); !errors.Is(err, transcription.ErrInvalidResponse) {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestRequestRejectsInvalidAudio(t *testing.T) {
	if _, err := transcription.NewRequest(nil); !errors.Is(err, transcription.ErrInvalidRequest) {
		t.Fatalf("NewRequest error = %v", err)
	}
	if _, err := transcription.NewRequest(&media.Media{}); !errors.Is(err, transcription.ErrInvalidRequest) {
		t.Fatalf("NewRequest error = %v", err)
	}
}

func TestRequestRejectsInvalidOptions(t *testing.T) {
	request := &transcription.Request{
		Audio:   audioFixture(t),
		Options: transcription.Options{Model: " padded "},
	}
	if err := request.Validate(); !errors.Is(err, transcription.ErrInvalidRequest) {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestOptionsResolveRejectsInvalidOverride(t *testing.T) {
	if _, err := (transcription.Options{}).Resolve(transcription.Options{Language: " en "}); !errors.Is(err, transcription.ErrInvalidOptions) {
		t.Fatalf("Resolve error = %v", err)
	}
}

func TestNewOutputRejectsInvalidMetadata(t *testing.T) {
	if _, err := transcription.NewOutput("text", metadata.Map{"": json.RawMessage(`1`)}); !errors.Is(err, transcription.ErrInvalidResponse) {
		t.Fatalf("NewOutput error = %v", err)
	}
}

func TestNewOutputClonesCallerMetadata(t *testing.T) {
	callerMetadata := metadata.Map{"provider/segment": json.RawMessage(`0`)}
	output, err := transcription.NewOutput("text", callerMetadata)
	if err != nil {
		t.Fatal(err)
	}
	callerMetadata["provider/segment"] = json.RawMessage(`1`)
	if string(output.Metadata["provider/segment"]) != "0" {
		t.Fatal("NewOutput aliases the caller metadata")
	}
}
