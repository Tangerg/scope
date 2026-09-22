package image_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

func imageFixture(t *testing.T) *media.Media {
	t.Helper()
	value, err := media.NewBytes("image/png", []byte("\x89PNG"))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func int64Pointer(value int64) *int64 { return &value }

func TestOptionsRoundTrip(t *testing.T) {
	options := image.Options{
		Model:          "dall-e-3",
		NegativePrompt: "blurry",
		Width:          int64Pointer(1024),
		Height:         int64Pointer(1024),
		Seed:           int64Pointer(0),
		OutputFormat:   "image/png",
		Extensions:     mustExtensions(t, map[string]any{"provider/style": "vivid"}),
	}
	encoded, err := jsonv2.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}

	var decoded image.Options
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Model != options.Model || decoded.NegativePrompt != options.NegativePrompt ||
		decoded.OutputFormat != options.OutputFormat || !decoded.Extensions.Equal(options.Extensions) {
		t.Fatalf("Options round trip = %#v, want %#v", decoded, options)
	}
	for name, pair := range map[string][2]*int64{
		"width":  {decoded.Width, options.Width},
		"height": {decoded.Height, options.Height},
		"seed":   {decoded.Seed, options.Seed},
	} {
		if pair[0] == nil || *pair[0] != *pair[1] {
			t.Fatalf("Options round trip lost %s: %v", name, pair[0])
		}
	}
}

// TestAbsentDimensionsStayAbsent proves the pointer fields carry presence
// rather than a zero-value default: an option the caller never set must not
// reappear as an explicit 0 on the wire.
func TestAbsentDimensionsStayAbsent(t *testing.T) {
	encoded, err := jsonv2.Marshal(image.Options{Model: "dall-e-3"})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := jsonv2.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"width", "height", "seed"} {
		if _, present := wire[field]; present {
			t.Errorf("unset %s was encoded as %v", field, wire[field])
		}
	}
}

func TestRequestRoundTrip(t *testing.T) {
	request, err := image.NewRequest("a duck on a lake")
	if err != nil {
		t.Fatal(err)
	}
	request.Options = image.Options{Model: "dall-e-3", Width: int64Pointer(512)}

	encoded, err := jsonv2.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	var decoded image.Request
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Prompt != "a duck on a lake" || decoded.Options.Width == nil || *decoded.Options.Width != 512 {
		t.Fatalf("Request round trip = %#v", decoded)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	output, err := image.NewOutput(imageFixture(t), metadata.Map{"provider/index": json.RawMessage(`0`)})
	if err != nil {
		t.Fatal(err)
	}
	response, err := image.NewResponse([]*image.Output{output}, &image.ResponseMetadata{
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

	var decoded image.Response
	if err = jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	first := decoded.First()
	if first == nil || first.Media == nil || first.Media.MIME != "image/png" {
		t.Fatalf("Response round trip lost media: %#v", first)
	}
	bytes, err := first.Media.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes) != "\x89PNG" {
		t.Fatalf("Response round trip lost image bytes: %q", bytes)
	}
	if decoded.Metadata == nil || !decoded.Metadata.CreatedAt.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("Response round trip lost metadata: %#v", decoded.Metadata)
	}
}

func TestOutputAndResponseMetadataRoundTrip(t *testing.T) {
	output, err := image.NewOutput(imageFixture(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	var decodedOutput image.Output
	if err = jsonv2.Unmarshal(encoded, &decodedOutput); err != nil {
		t.Fatal(err)
	}
	if decodedOutput.Media == nil || decodedOutput.Media.MIME != "image/png" {
		t.Fatalf("Output round trip = %#v", decodedOutput)
	}

	responseMetadata := image.ResponseMetadata{CreatedAt: time.Unix(7, 0).UTC()}
	encoded, err = jsonv2.Marshal(responseMetadata)
	if err != nil {
		t.Fatal(err)
	}
	var decodedMetadata image.ResponseMetadata
	if err := jsonv2.Unmarshal(encoded, &decodedMetadata); err != nil {
		t.Fatal(err)
	}
	if decodedMetadata.CreatedAt != responseMetadata.CreatedAt {
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
		"options":           {target: new(image.Options), want: image.ErrInvalidOptions},
		"request":           {target: new(image.Request), want: image.ErrInvalidRequest},
		"output":            {target: new(image.Output), want: image.ErrInvalidResponse},
		"response metadata": {target: new(image.ResponseMetadata), want: image.ErrInvalidResponse},
		"response":          {target: new(image.Response), want: image.ErrInvalidResponse},
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
		"options":           {target: (*image.Options)(nil), want: image.ErrInvalidOptions},
		"request":           {target: (*image.Request)(nil), want: image.ErrInvalidRequest},
		"output":            {target: (*image.Output)(nil), want: image.ErrInvalidResponse},
		"response metadata": {target: (*image.ResponseMetadata)(nil), want: image.ErrInvalidResponse},
		"response":          {target: (*image.Response)(nil), want: image.ErrInvalidResponse},
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
	options := image.Options{Model: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"width":0}`), &options); !errors.Is(err, image.ErrInvalidOptions) {
		t.Fatalf("Options decode error = %v", err)
	}
	if options.Model != "keep" || options.Width != nil {
		t.Fatalf("failed Options decode mutated receiver: %#v", options)
	}

	request := image.Request{Prompt: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"prompt":""}`), &request); !errors.Is(err, image.ErrInvalidRequest) {
		t.Fatalf("Request decode error = %v", err)
	}
	if request.Prompt != "keep" {
		t.Fatalf("failed Request decode mutated receiver: %#v", request)
	}

	output := image.Output{Media: imageFixture(t)}
	if err := jsonv2.Unmarshal([]byte(`{"media":null}`), &output); !errors.Is(err, image.ErrInvalidResponse) {
		t.Fatalf("Output decode error = %v", err)
	}
	if output.Media == nil {
		t.Fatalf("failed Output decode mutated receiver: %#v", output)
	}

	responseMetadata := image.ResponseMetadata{CreatedAt: time.Unix(7, 0).UTC()}
	if err := jsonv2.Unmarshal([]byte(`{"created_at":"not-a-time"}`), &responseMetadata); !errors.Is(err, image.ErrInvalidResponse) {
		t.Fatalf("ResponseMetadata decode error = %v", err)
	}
	if !responseMetadata.CreatedAt.Equal(time.Unix(7, 0)) {
		t.Fatalf("failed ResponseMetadata decode mutated receiver: %#v", responseMetadata)
	}

	kept := image.Output{Media: imageFixture(t)}
	response := image.Response{Outputs: []*image.Output{&kept}}
	if err := jsonv2.Unmarshal([]byte(`{"outputs":[]}`), &response); !errors.Is(err, image.ErrInvalidResponse) {
		t.Fatalf("Response decode error = %v", err)
	}
	if len(response.Outputs) != 1 {
		t.Fatalf("failed Response decode mutated receiver: %#v", response)
	}
}

func TestInvalidValuesFailToMarshal(t *testing.T) {
	brokenExtra := metadata.Map{"provider/broken": json.RawMessage(`{`)}
	cases := map[string]struct {
		value any
		want  error
	}{
		"padded options model":    {value: image.Options{Model: " padded "}, want: image.ErrInvalidOptions},
		"non positive width":      {value: image.Options{Width: int64Pointer(0)}, want: image.ErrInvalidOptions},
		"non positive height":     {value: image.Options{Height: int64Pointer(-1)}, want: image.ErrInvalidOptions},
		"negative seed":           {value: image.Options{Seed: int64Pointer(-1)}, want: image.ErrInvalidOptions},
		"empty prompt":            {value: image.Request{}, want: image.ErrInvalidRequest},
		"missing output media":    {value: image.Output{}, want: image.ErrInvalidResponse},
		"invalid extra":           {value: image.ResponseMetadata{Extra: brokenExtra}, want: image.ErrInvalidResponse},
		"response without output": {value: image.Response{}, want: image.ErrInvalidResponse},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := jsonv2.Marshal(testCase.value); !errors.Is(err, testCase.want) {
				t.Fatalf("Marshal error = %v, want %v", err, testCase.want)
			}
		})
	}
}

// TestOutputFormatMustBeCanonicalImageMIME pins every rejection reason of the
// output-format rule, because a provider that accepts a non-canonical form here
// would silently disagree with the media MIME check on the response side.
func TestOutputFormatMustBeCanonicalImageMIME(t *testing.T) {
	for name, format := range map[string]string{
		"unparsable":       "image//png",
		"not an image":     "application/json",
		"bare image":       "image/",
		"with parameters":  "image/png; quality=90",
		"non canonical":    "IMAGE/PNG",
		"leading garbling": "  image/png",
	} {
		t.Run(name, func(t *testing.T) {
			if err := (image.Options{OutputFormat: format}).Validate(); !errors.Is(err, image.ErrInvalidOptions) {
				t.Fatalf("Validate(%q) error = %v", format, err)
			}
		})
	}
	if err := (image.Options{OutputFormat: "image/png"}).Validate(); err != nil {
		t.Fatalf("Validate rejected a canonical image MIME type: %v", err)
	}
}

// TestOutputRejectsNonImageMedia guards the response side of the same rule: a
// provider must not smuggle a JSON or text payload through the image protocol.
func TestOutputRejectsNonImageMedia(t *testing.T) {
	text, err := media.NewBytes("text/plain", []byte("not an image"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = image.NewOutput(text, nil); !errors.Is(err, image.ErrInvalidResponse) {
		t.Fatalf("NewOutput error = %v", err)
	}

	binary, err := media.NewBytes("application/octet-stream", []byte("opaque"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := image.NewOutput(binary, nil); err != nil {
		t.Fatalf("NewOutput rejected an opaque binary payload: %v", err)
	}
}

func TestOutputRejectsInvalidMetadata(t *testing.T) {
	_, err := image.NewOutput(imageFixture(t), metadata.Map{"provider/broken": json.RawMessage(`{`)})
	if !errors.Is(err, image.ErrInvalidResponse) {
		t.Fatalf("NewOutput error = %v", err)
	}
}

func TestNilValidateIsRejected(t *testing.T) {
	if err := (*image.Response)(nil).Validate(); !errors.Is(err, image.ErrInvalidResponse) {
		t.Fatalf("Validate error = %v", err)
	}
	if err := (*image.Output)(nil).Validate(); !errors.Is(err, image.ErrInvalidResponse) {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestNewResponseDoesNotAliasCallerSlice(t *testing.T) {
	output, err := image.NewOutput(imageFixture(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	outputs := []*image.Output{output}
	response, err := image.NewResponse(outputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	outputs[0] = nil
	if response.First() != output {
		t.Fatal("NewResponse aliases the caller outputs slice")
	}
}

func TestNewOutputClonesCallerMetadata(t *testing.T) {
	callerMetadata := metadata.Map{"provider/index": json.RawMessage(`0`)}
	output, err := image.NewOutput(imageFixture(t), callerMetadata)
	if err != nil {
		t.Fatal(err)
	}
	callerMetadata["provider/index"] = json.RawMessage(`1`)
	if string(output.Metadata["provider/index"]) != "0" {
		t.Fatal("NewOutput aliases the caller metadata")
	}
}

func TestOptionsResolveRejectsInvalidOverride(t *testing.T) {
	if _, err := (image.Options{}).Resolve(image.Options{Width: int64Pointer(-1)}); !errors.Is(err, image.ErrInvalidOptions) {
		t.Fatalf("Resolve error = %v", err)
	}
}

// TestOptionsResolveClonesPointerFields keeps Resolve from handing the caller a
// pointer that still aliases the override, which would let a later mutation
// change an already-resolved request.
func TestOptionsResolveClonesPointerFields(t *testing.T) {
	override := image.Options{Width: int64Pointer(512), Height: int64Pointer(512), Seed: int64Pointer(7)}
	resolved, err := (image.Options{}).Resolve(override)
	if err != nil {
		t.Fatal(err)
	}
	*override.Width = 1024
	*override.Height = 1024
	*override.Seed = 9
	if *resolved.Width != 512 || *resolved.Height != 512 || *resolved.Seed != 7 {
		t.Fatalf("Resolve aliases override pointers: %#v", resolved)
	}
}
