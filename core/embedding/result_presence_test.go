package embedding_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
)

func TestResponseJSONRequiresNumericCoordinates(t *testing.T) {
	for _, vector := range []string{`[null]`, `[1,null,2]`} {
		kept := &embedding.Output{Embedding: []float64{0.5}}
		response := embedding.Response{Outputs: []*embedding.Output{kept}}
		if err := jsonv2.Unmarshal([]byte(`{"outputs":[{"embedding":`+vector+`}]}`), &response); !errors.Is(err, embedding.ErrInvalidResponse) {
			t.Fatalf("decode %s error = %v, want ErrInvalidResponse", vector, err)
		}
		if response.First() != kept || !slices.Equal(kept.Embedding, []float64{0.5}) {
			t.Fatal("rejected vector changed the retained response")
		}
	}
	var response embedding.Response
	data := `{"outputs":[{"embedding":[1,0,2]}]}`
	if err := jsonv2.Unmarshal([]byte(data), &response); err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(response)
	if err != nil || string(encoded) != data {
		t.Fatalf("round trip = %s, %v; want %s", encoded, err, data)
	}
}

func TestUsageJSONRequiresReportedTokens(t *testing.T) {
	for _, data := range []string{`null`, `{}`, `{"input_tokens":null}`} {
		usage := embedding.Usage{InputTokens: 7}
		if err := jsonv2.Unmarshal([]byte(data), &usage); !errors.Is(err, embedding.ErrInvalidResponse) {
			t.Fatalf("decode %s error = %v, want ErrInvalidResponse", data, err)
		}
		if usage.InputTokens != 7 {
			t.Fatalf("rejected usage changed receiver to %v", usage)
		}
	}
	for _, data := range []string{`{}`, `{"usage":null}`, `{"usage":{"input_tokens":0}}`} {
		var responseMetadata embedding.ResponseMetadata
		if err := jsonv2.Unmarshal([]byte(data), &responseMetadata); err != nil {
			t.Fatal(err)
		}
		wantReported := data == `{"usage":{"input_tokens":0}}`
		if (responseMetadata.Usage != nil) != wantReported || (wantReported && responseMetadata.Usage.InputTokens != 0) {
			t.Fatalf("decoded %s as %#v", data, responseMetadata.Usage)
		}
	}
	var response embedding.Response
	if err := jsonv2.Unmarshal([]byte(`{"outputs":[{"embedding":[1]}],"metadata":{"usage":{}}}`), &response); !errors.Is(err, embedding.ErrInvalidResponse) {
		t.Fatalf("nested usage decode error = %v, want ErrInvalidResponse", err)
	}
}
