package embedding_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
)

func int64Pointer(value int64) *int64 { return &value }

func TestOptionsRoundTrip(t *testing.T) {
	options := embedding.Options{
		Model:      "text-embedding-3-small",
		Dimensions: int64Pointer(256),
		Extensions: mustExtensions(t, map[string]any{"provider/encoding": "float"}),
	}
	encoded, err := jsonv2.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}

	var decoded embedding.Options
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Model != options.Model || decoded.Dimensions == nil || *decoded.Dimensions != 256 ||
		!decoded.Extensions.Equal(options.Extensions) {
		t.Fatalf("Options round trip = %#v, want %#v", decoded, options)
	}
}

// TestAbsentDimensionsStayAbsent proves the pointer field carries presence
// rather than a zero-value default: dimensions the caller never set must not
// reappear as an explicit 0 on the wire.
func TestAbsentDimensionsStayAbsent(t *testing.T) {
	encoded, err := jsonv2.Marshal(embedding.Options{Model: "text-embedding-3-small"})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := jsonv2.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if _, present := wire["dimensions"]; present {
		t.Errorf("unset dimensions was encoded as %v", wire["dimensions"])
	}
}

func TestRequestRoundTrip(t *testing.T) {
	request, err := embedding.NewRequest([]string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	request.Options = embedding.Options{Model: "text-embedding-3-small"}

	encoded, err := jsonv2.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	var decoded embedding.Request
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Texts) != 2 || decoded.Texts[1] != "second" ||
		decoded.Options.Model != "text-embedding-3-small" {
		t.Fatalf("Request round trip = %#v", decoded)
	}
}

func TestOutputAndUsageRoundTrip(t *testing.T) {
	output, err := embedding.NewOutput([]float64{0.5, -0.25}, metadata.Map{"provider/index": json.RawMessage(`0`)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	var decodedOutput embedding.Output
	if err = jsonv2.Unmarshal(encoded, &decodedOutput); err != nil {
		t.Fatal(err)
	}
	if len(decodedOutput.Embedding) != 2 || decodedOutput.Embedding[1] != -0.25 {
		t.Fatalf("Output round trip = %#v", decodedOutput)
	}

	usage := embedding.Usage{InputTokens: 42}
	encoded, err = jsonv2.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	var decodedUsage embedding.Usage
	if err := jsonv2.Unmarshal(encoded, &decodedUsage); err != nil {
		t.Fatal(err)
	}
	if decodedUsage != usage {
		t.Fatalf("Usage round trip = %#v, want %#v", decodedUsage, usage)
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
		"options":           {target: new(embedding.Options), want: embedding.ErrInvalidOptions},
		"request":           {target: new(embedding.Request), want: embedding.ErrInvalidRequest},
		"output":            {target: new(embedding.Output), want: embedding.ErrInvalidResponse},
		"usage":             {target: new(embedding.Usage), want: embedding.ErrInvalidResponse},
		"response metadata": {target: new(embedding.ResponseMetadata), want: embedding.ErrInvalidResponse},
		"response":          {target: new(embedding.Response), want: embedding.ErrInvalidResponse},
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
		"options":           {target: (*embedding.Options)(nil), want: embedding.ErrInvalidOptions},
		"request":           {target: (*embedding.Request)(nil), want: embedding.ErrInvalidRequest},
		"output":            {target: (*embedding.Output)(nil), want: embedding.ErrInvalidResponse},
		"usage":             {target: (*embedding.Usage)(nil), want: embedding.ErrInvalidResponse},
		"response metadata": {target: (*embedding.ResponseMetadata)(nil), want: embedding.ErrInvalidResponse},
		"response":          {target: (*embedding.Response)(nil), want: embedding.ErrInvalidResponse},
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
	options := embedding.Options{Model: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"dimensions":0}`), &options); !errors.Is(err, embedding.ErrInvalidOptions) {
		t.Fatalf("Options decode error = %v", err)
	}
	if options.Model != "keep" || options.Dimensions != nil {
		t.Fatalf("failed Options decode mutated receiver: %#v", options)
	}

	output := embedding.Output{Embedding: []float64{1}}
	if err := jsonv2.Unmarshal([]byte(`{"embedding":[]}`), &output); !errors.Is(err, embedding.ErrInvalidResponse) {
		t.Fatalf("Output decode error = %v", err)
	}
	if len(output.Embedding) != 1 {
		t.Fatalf("failed Output decode mutated receiver: %#v", output)
	}

	usage := embedding.Usage{InputTokens: 7}
	if err := jsonv2.Unmarshal([]byte(`{"input_tokens":-1}`), &usage); !errors.Is(err, embedding.ErrInvalidResponse) {
		t.Fatalf("Usage decode error = %v", err)
	}
	if usage.InputTokens != 7 {
		t.Fatalf("failed Usage decode mutated receiver: %#v", usage)
	}

	responseMetadata := embedding.ResponseMetadata{Model: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"created_at":"not-a-time"}`), &responseMetadata); !errors.Is(err, embedding.ErrInvalidResponse) {
		t.Fatalf("ResponseMetadata decode error = %v", err)
	}
	if responseMetadata.Model != "keep" {
		t.Fatalf("failed ResponseMetadata decode mutated receiver: %#v", responseMetadata)
	}
}

func TestInvalidValuesFailToMarshal(t *testing.T) {
	brokenExtra := metadata.Map{"provider/broken": json.RawMessage(`{`)}
	cases := map[string]struct {
		value any
		want  error
	}{
		"padded options model":    {value: embedding.Options{Model: " padded "}, want: embedding.ErrInvalidOptions},
		"non positive dimensions": {value: embedding.Options{Dimensions: int64Pointer(0)}, want: embedding.ErrInvalidOptions},
		"empty texts":             {value: embedding.Request{}, want: embedding.ErrInvalidRequest},
		"empty embedding":         {value: embedding.Output{}, want: embedding.ErrInvalidResponse},
		"invalid output metadata": {value: embedding.Output{Embedding: []float64{1}, Metadata: brokenExtra}, want: embedding.ErrInvalidResponse},
		"negative input tokens":   {value: embedding.Usage{InputTokens: -1}, want: embedding.ErrInvalidResponse},
		"padded metadata model":   {value: embedding.ResponseMetadata{Model: " padded "}, want: embedding.ErrInvalidResponse},
		"invalid extra":           {value: embedding.ResponseMetadata{Extra: brokenExtra}, want: embedding.ErrInvalidResponse},
		"negative usage":          {value: embedding.ResponseMetadata{Usage: &embedding.Usage{InputTokens: -1}}, want: embedding.ErrInvalidResponse},
		"response without output": {value: embedding.Response{}, want: embedding.ErrInvalidResponse},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := jsonv2.Marshal(testCase.value); !errors.Is(err, testCase.want) {
				t.Fatalf("Marshal error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestOutputRejectsNonFiniteComponents(t *testing.T) {
	for name, value := range map[string]float64{
		"nan":               math.NaN(),
		"positive infinity": math.Inf(1),
		"negative infinity": math.Inf(-1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := embedding.NewOutput([]float64{0.1, value}, nil); !errors.Is(err, embedding.ErrInvalidResponse) {
				t.Fatalf("NewOutput(%v) error = %v", value, err)
			}
		})
	}
}

// TestResponseRequiresUniformDimensions is the invariant a caller relies on to
// treat the outputs as one matrix: a provider must not mix vector widths inside
// a single response.
func TestResponseRequiresUniformDimensions(t *testing.T) {
	wide, err := embedding.NewOutput([]float64{1, 2, 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := embedding.NewOutput([]float64{1, 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := embedding.NewResponse([]*embedding.Output{wide, narrow}, nil); !errors.Is(err, embedding.ErrInvalidResponse) {
		t.Fatalf("NewResponse error = %v", err)
	}
	if _, err := embedding.NewResponse([]*embedding.Output{wide, wide}, nil); err != nil {
		t.Fatalf("NewResponse rejected uniform dimensions: %v", err)
	}
}

func TestNilValidateIsRejected(t *testing.T) {
	if err := (*embedding.Request)(nil).Validate(); !errors.Is(err, embedding.ErrInvalidRequest) {
		t.Fatalf("Validate error = %v", err)
	}
	if err := (*embedding.Response)(nil).Validate(); !errors.Is(err, embedding.ErrInvalidResponse) {
		t.Fatalf("Validate error = %v", err)
	}
	if err := (*embedding.Output)(nil).Validate(); !errors.Is(err, embedding.ErrInvalidResponse) {
		t.Fatalf("Validate error = %v", err)
	}
	if (*embedding.Response)(nil).First() != nil || (&embedding.Response{}).First() != nil {
		t.Fatal("empty response returned an output")
	}
}

func TestNewResponseDoesNotAliasCallerSlice(t *testing.T) {
	output, err := embedding.NewOutput([]float64{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	outputs := []*embedding.Output{output}
	response, err := embedding.NewResponse(outputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	outputs[0] = nil
	if response.First() != output {
		t.Fatal("NewResponse aliases the caller outputs slice")
	}
}

func TestOptionsResolveRejectsInvalidOverride(t *testing.T) {
	if _, err := (embedding.Options{}).Resolve(embedding.Options{Dimensions: int64Pointer(-1)}); !errors.Is(err, embedding.ErrInvalidOptions) {
		t.Fatalf("Resolve error = %v", err)
	}
}

// TestOptionsResolveClonesPointerFields keeps Resolve from handing the caller a
// pointer that still aliases the override, which would let a later mutation
// change an already-resolved request.
func TestOptionsResolveClonesPointerFields(t *testing.T) {
	override := embedding.Options{Dimensions: int64Pointer(256)}
	resolved, err := (embedding.Options{}).Resolve(override)
	if err != nil {
		t.Fatal(err)
	}
	*override.Dimensions = 512
	if *resolved.Dimensions != 256 {
		t.Fatalf("Resolve aliases the override pointer: %d", *resolved.Dimensions)
	}
}
