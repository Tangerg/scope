package rerank_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/rerank"
)

func TestResponseJSONRequiresCompleteResults(t *testing.T) {
	for _, result := range []string{
		`{}`, `{"index":0}`, `{"score":0}`,
		`{"index":null,"score":0}`, `{"index":0,"score":null}`,
	} {
		t.Run(result, func(t *testing.T) {
			kept := &rerank.Result{Index: 2, Score: 0.8}
			response := rerank.Response{Results: []*rerank.Result{kept}}
			if err := jsonv2.Unmarshal([]byte(`{"results":[`+result+`]}`), &response); !errors.Is(err, rerank.ErrInvalidResponse) {
				t.Fatalf("decode error = %v, want ErrInvalidResponse", err)
			}
			if response.First() != kept || *kept != (rerank.Result{Index: 2, Score: 0.8}) {
				t.Fatal("rejected result changed the retained response")
			}
		})
	}
	var response rerank.Response
	data := `{"results":[{"index":0,"score":0}]}`
	if err := jsonv2.Unmarshal([]byte(data), &response); err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(response)
	if err != nil || string(encoded) != data {
		t.Fatalf("round trip = %s, %v; want %s", encoded, err, data)
	}
}

func TestScoreJSONRequiresANumber(t *testing.T) {
	score := rerank.Score(0.8)
	if err := jsonv2.Unmarshal([]byte(`null`), &score); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("decode error = %v, want ErrInvalidResponse", err)
	}
	if score != 0.8 {
		t.Fatalf("rejected score changed receiver to %v", score)
	}
}

func TestUsageJSONRequiresReportedTokens(t *testing.T) {
	for _, data := range []string{`null`, `{}`, `{"input_tokens":null}`} {
		usage := rerank.Usage{InputTokens: 7}
		if err := jsonv2.Unmarshal([]byte(data), &usage); !errors.Is(err, rerank.ErrInvalidResponse) {
			t.Fatalf("decode %s error = %v, want ErrInvalidResponse", data, err)
		}
		if usage.InputTokens != 7 {
			t.Fatalf("rejected usage changed receiver to %v", usage)
		}
	}
	for _, data := range []string{`{}`, `{"usage":null}`, `{"usage":{"input_tokens":0}}`} {
		var responseMetadata rerank.ResponseMetadata
		if err := jsonv2.Unmarshal([]byte(data), &responseMetadata); err != nil {
			t.Fatal(err)
		}
		wantReported := data == `{"usage":{"input_tokens":0}}`
		if (responseMetadata.Usage != nil) != wantReported || (wantReported && responseMetadata.Usage.InputTokens != 0) {
			t.Fatalf("decoded %s as %#v", data, responseMetadata.Usage)
		}
	}
	var response rerank.Response
	if err := jsonv2.Unmarshal([]byte(`{"results":[{"index":0,"score":0}],"metadata":{"usage":{}}}`), &response); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("nested usage decode error = %v, want ErrInvalidResponse", err)
	}
}
