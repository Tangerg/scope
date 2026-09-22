package moderation_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/moderation"
)

func TestOptionsRoundTrip(t *testing.T) {
	options := moderation.Options{
		Model:      "moderation-model",
		Extensions: mustExtensions(t, map[string]any{"provider/threshold": 0.5}),
	}
	encoded, err := jsonv2.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}

	var decoded moderation.Options
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Model != options.Model || !decoded.Extensions.Equal(options.Extensions) {
		t.Fatalf("Options round trip = %#v, want %#v", decoded, options)
	}
}

func TestRequestRoundTrip(t *testing.T) {
	request, err := moderation.NewRequest([]string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	request.Options = moderation.Options{Model: "moderation-model"}

	encoded, err := jsonv2.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	var decoded moderation.Request
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Texts) != 2 || decoded.Texts[1] != "second" || decoded.Options.Model != "moderation-model" {
		t.Fatalf("Request round trip = %#v", decoded)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	output, err := moderation.NewOutput(
		moderation.Categories{"violence": {Flagged: true, Score: 0.91}},
		metadata.Map{"provider/index": json.RawMessage(`0`)},
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := moderation.NewResponse([]*moderation.Output{output}, &moderation.ResponseMetadata{
		ID:        "modr-1",
		Model:     "moderation-model",
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

	var decoded moderation.Response
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	first := decoded.First()
	if first == nil || !first.Categories.Flagged() || first.Categories["violence"].Score != 0.91 {
		t.Fatalf("Response round trip lost categories: %#v", decoded)
	}
	if decoded.Metadata == nil || decoded.Metadata.ID != "modr-1" || !decoded.Metadata.CreatedAt.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("Response round trip lost metadata: %#v", decoded.Metadata)
	}
}

func TestVerdictAndCategoriesRoundTrip(t *testing.T) {
	verdict := moderation.Verdict{Flagged: true, Score: 1}
	encoded, err := jsonv2.Marshal(verdict)
	if err != nil {
		t.Fatal(err)
	}
	var decodedVerdict moderation.Verdict
	if err = jsonv2.Unmarshal(encoded, &decodedVerdict); err != nil {
		t.Fatal(err)
	}
	if decodedVerdict != verdict {
		t.Fatalf("Verdict round trip = %#v, want %#v", decodedVerdict, verdict)
	}

	categories := moderation.Categories{"hate": {Score: 0.25}}
	encoded, err = jsonv2.Marshal(categories)
	if err != nil {
		t.Fatal(err)
	}
	var decodedCategories moderation.Categories
	if err := jsonv2.Unmarshal(encoded, &decodedCategories); err != nil {
		t.Fatal(err)
	}
	if decodedCategories["hate"].Score != 0.25 {
		t.Fatalf("Categories round trip = %#v", decodedCategories)
	}
}

func TestResponseMetadataRoundTrip(t *testing.T) {
	responseMetadata := moderation.ResponseMetadata{
		ID:        "id",
		Model:     "model",
		CreatedAt: time.Unix(7, 0).UTC(),
		Extra:     metadata.Map{"provider/region": json.RawMessage(`"eu"`)},
	}
	encoded, err := jsonv2.Marshal(responseMetadata)
	if err != nil {
		t.Fatal(err)
	}
	var decoded moderation.ResponseMetadata
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ID != responseMetadata.ID ||
		decoded.Model != responseMetadata.Model ||
		decoded.CreatedAt != responseMetadata.CreatedAt ||
		!decoded.Extra.Equal(responseMetadata.Extra) {
		t.Fatalf("ResponseMetadata round trip = %#v, want %#v", decoded, responseMetadata)
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
		"options":           {target: new(moderation.Options), want: moderation.ErrInvalidOptions},
		"request":           {target: new(moderation.Request), want: moderation.ErrInvalidRequest},
		"verdict":           {target: new(moderation.Verdict), want: moderation.ErrInvalidResponse},
		"categories":        {target: new(moderation.Categories), want: moderation.ErrInvalidResponse},
		"output":            {target: new(moderation.Output), want: moderation.ErrInvalidResponse},
		"response metadata": {target: new(moderation.ResponseMetadata), want: moderation.ErrInvalidResponse},
		"response":          {target: new(moderation.Response), want: moderation.ErrInvalidResponse},
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
		"options":           {target: (*moderation.Options)(nil), want: moderation.ErrInvalidOptions},
		"request":           {target: (*moderation.Request)(nil), want: moderation.ErrInvalidRequest},
		"verdict":           {target: (*moderation.Verdict)(nil), want: moderation.ErrInvalidResponse},
		"categories":        {target: (*moderation.Categories)(nil), want: moderation.ErrInvalidResponse},
		"output":            {target: (*moderation.Output)(nil), want: moderation.ErrInvalidResponse},
		"response metadata": {target: (*moderation.ResponseMetadata)(nil), want: moderation.ErrInvalidResponse},
		"response":          {target: (*moderation.Response)(nil), want: moderation.ErrInvalidResponse},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if err := testCase.target.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, testCase.want) {
				t.Fatalf("UnmarshalJSON error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestVerdictRejectsNonFiniteScores(t *testing.T) {
	for name, score := range map[string]float64{
		"nan":               math.NaN(),
		"positive infinity": math.Inf(1),
		"negative infinity": math.Inf(-1),
		"below range":       -math.SmallestNonzeroFloat64,
		"above range":       1.0000001,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := moderation.NewOutput(moderation.Categories{"c": {Score: score}}, nil); !errors.Is(err, moderation.ErrInvalidResponse) {
				t.Fatalf("NewOutput(%v) error = %v", score, err)
			}
		})
	}
}

func TestCategoriesRejectInvalidKeys(t *testing.T) {
	for name, key := range map[string]string{
		"empty":               "",
		"leading whitespace":  " hate",
		"trailing whitespace": "hate ",
	} {
		t.Run(name, func(t *testing.T) {
			categories := moderation.Categories{key: {}}
			if _, err := jsonv2.Marshal(categories); !errors.Is(err, moderation.ErrInvalidResponse) {
				t.Fatalf("Marshal error = %v", err)
			}
		})
	}
}

func TestResponseMetadataValidation(t *testing.T) {
	cases := map[string]moderation.ResponseMetadata{
		"padded id":     {ID: " id "},
		"padded model":  {Model: " model "},
		"invalid extra": {Extra: metadata.Map{"broken": json.RawMessage(`{`)}},
	}
	for name, responseMetadata := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := jsonv2.Marshal(responseMetadata); !errors.Is(err, moderation.ErrInvalidResponse) {
				t.Fatalf("Marshal error = %v", err)
			}
			output, err := moderation.NewOutput(moderation.Categories{"safe": {}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := moderation.NewResponse([]*moderation.Output{output}, &responseMetadata); !errors.Is(err, moderation.ErrInvalidResponse) {
				t.Fatalf("NewResponse error = %v", err)
			}
		})
	}
}

func TestOutputRejectsInvalidMetadata(t *testing.T) {
	_, err := moderation.NewOutput(
		moderation.Categories{"safe": {}},
		metadata.Map{"provider/broken": json.RawMessage(`{`)},
	)
	if !errors.Is(err, moderation.ErrInvalidResponse) {
		t.Fatalf("NewOutput error = %v", err)
	}
}

func TestResponseRejectsInvalidOutput(t *testing.T) {
	if _, err := moderation.NewResponse([]*moderation.Output{nil}, nil); !errors.Is(err, moderation.ErrInvalidResponse) {
		t.Fatalf("NewResponse error = %v", err)
	}
	invalid := &moderation.Output{Categories: moderation.Categories{"safe": {Score: 2}}}
	if _, err := moderation.NewResponse([]*moderation.Output{invalid}, nil); !errors.Is(err, moderation.ErrInvalidResponse) {
		t.Fatalf("NewResponse error = %v", err)
	}
}

func TestNewResponseDoesNotAliasCallerSlice(t *testing.T) {
	output, err := moderation.NewOutput(moderation.Categories{"safe": {}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	outputs := []*moderation.Output{output}
	response, err := moderation.NewResponse(outputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	outputs[0] = nil
	if response.First() != output {
		t.Fatal("NewResponse aliases the caller outputs slice")
	}
}

func TestNewRequestDoesNotAliasCallerSlice(t *testing.T) {
	texts := []string{"first"}
	request, err := moderation.NewRequest(texts)
	if err != nil {
		t.Fatal(err)
	}
	texts[0] = "mutated"
	if request.Texts[0] != "first" {
		t.Fatal("NewRequest aliases the caller texts slice")
	}
}

// TestDecodedValuesAreValidatedBeforeAssignment covers the branch where the
// payload is syntactically valid JSON but violates the protocol: the receiver
// must keep its previous value.
func TestDecodedValuesAreValidatedBeforeAssignment(t *testing.T) {
	categories := moderation.Categories{"keep": {}}
	if err := jsonv2.Unmarshal([]byte(`{" padded ":{"flagged":false,"score":0}}`), &categories); !errors.Is(err, moderation.ErrInvalidResponse) {
		t.Fatalf("Categories decode error = %v", err)
	}
	if _, kept := categories["keep"]; !kept {
		t.Fatalf("failed Categories decode mutated receiver: %#v", categories)
	}

	output := moderation.Output{Categories: moderation.Categories{"keep": {}}}
	if err := jsonv2.Unmarshal([]byte(`{"categories":{}}`), &output); !errors.Is(err, moderation.ErrInvalidResponse) {
		t.Fatalf("Output decode error = %v", err)
	}
	if _, kept := output.Categories["keep"]; !kept {
		t.Fatalf("failed Output decode mutated receiver: %#v", output)
	}

	responseMetadata := moderation.ResponseMetadata{ID: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"id":" padded "}`), &responseMetadata); !errors.Is(err, moderation.ErrInvalidResponse) {
		t.Fatalf("ResponseMetadata decode error = %v", err)
	}
	if responseMetadata.ID != "keep" {
		t.Fatalf("failed ResponseMetadata decode mutated receiver: %#v", responseMetadata)
	}
}

func TestOutputMarshalRejectsInvalidCategories(t *testing.T) {
	output := moderation.Output{Categories: moderation.Categories{"hate": {Score: 2}}}
	if _, err := jsonv2.Marshal(output); !errors.Is(err, moderation.ErrInvalidResponse) {
		t.Fatalf("Marshal error = %v", err)
	}
}

func TestNilResponseValidateIsRejected(t *testing.T) {
	if err := (*moderation.Response)(nil).Validate(); !errors.Is(err, moderation.ErrInvalidResponse) {
		t.Fatalf("Validate error = %v", err)
	}
	if err := (*moderation.Output)(nil).Validate(); !errors.Is(err, moderation.ErrInvalidResponse) {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestOptionsResolveRejectsInvalidOverride(t *testing.T) {
	if _, err := (moderation.Options{}).Resolve(moderation.Options{Model: " padded "}); !errors.Is(err, moderation.ErrInvalidOptions) {
		t.Fatalf("Resolve error = %v", err)
	}
}
