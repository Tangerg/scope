package moderation_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/moderation"
)

func TestJSONBoundaries(t *testing.T) {
	if err := (moderation.Options{Model: " model "}).Validate(); !errors.Is(err, moderation.ErrInvalidOptions) {
		t.Fatalf("NewOptions error = %v", err)
	}
	if _, err := moderation.NewRequest(nil); !errors.Is(err, moderation.ErrInvalidRequest) {
		t.Fatalf("NewRequest error = %v", err)
	}
	if _, err := moderation.NewResponse(nil, &moderation.ResponseMetadata{}); !errors.Is(err, moderation.ErrInvalidResponse) {
		t.Fatalf("NewResponse error = %v", err)
	}
	var extensionOptions moderation.Options
	if err := extensionOptions.Extensions.Set("invalid", true); err == nil {
		t.Fatalf("SetExtension error = %v", err)
	}

	if _, err := jsonv2.Marshal(moderation.Options{Model: " invalid "}); !errors.Is(err, moderation.ErrInvalidOptions) {
		t.Fatalf("Marshal Options error = %v", err)
	}
	if _, err := jsonv2.Marshal(moderation.Request{}); !errors.Is(err, moderation.ErrInvalidRequest) {
		t.Fatalf("Marshal Request error = %v", err)
	}
	if _, err := jsonv2.Marshal(moderation.Verdict{Score: 2}); !errors.Is(err, moderation.ErrInvalidResponse) {
		t.Fatalf("Marshal Verdict error = %v", err)
	}
	if _, err := jsonv2.Marshal(moderation.Response{}); !errors.Is(err, moderation.ErrInvalidResponse) {
		t.Fatalf("Marshal Response error = %v", err)
	}

	options := moderation.Options{Model: "keep"}
	if err := jsonv2.Unmarshal([]byte(`{"model":" invalid "}`), &options); !errors.Is(err, moderation.ErrInvalidOptions) {
		t.Fatalf("Unmarshal Options error = %v", err)
	}
	if options.Model != "keep" {
		t.Fatalf("failed Options decode mutated receiver: %#v", options)
	}

	request := moderation.Request{Texts: []string{"keep"}}
	if err := jsonv2.Unmarshal([]byte(`{"texts":[]}`), &request); !errors.Is(err, moderation.ErrInvalidRequest) {
		t.Fatalf("Unmarshal Request error = %v", err)
	}
	if len(request.Texts) != 1 || request.Texts[0] != "keep" {
		t.Fatalf("failed Request decode mutated receiver: %#v", request)
	}

	output, err := moderation.NewOutput(
		moderation.Categories{"safe": {}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response := moderation.Response{
		Outputs:  []*moderation.Output{output},
		Metadata: &moderation.ResponseMetadata{},
	}
	if err := jsonv2.Unmarshal([]byte(`{"outputs":[],"metadata":{}}`), &response); !errors.Is(err, moderation.ErrInvalidResponse) {
		t.Fatalf("Unmarshal Response error = %v", err)
	}
	if response.First() != output {
		t.Fatalf("failed Response decode mutated receiver: %#v", response)
	}

	verdict := moderation.Verdict{Score: 0.25}
	if err := jsonv2.Unmarshal([]byte(`{"score":2}`), &verdict); !errors.Is(err, moderation.ErrInvalidResponse) {
		t.Fatalf("Unmarshal Verdict error = %v", err)
	}
	if verdict.Score != 0.25 {
		t.Fatalf("failed Verdict decode mutated receiver: %#v", verdict)
	}
}
