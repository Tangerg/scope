package rerank_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/rerank"
)

func TestModelFuncAdaptsCall(t *testing.T) {
	request, err := rerank.NewRequest("query", []string{"first"})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	model := rerank.ModelFunc(func(_ context.Context, actual *rerank.Request) (*rerank.Response, error) {
		called = true
		if actual != request {
			t.Fatal("ModelFunc received a different request")
		}
		return nil, nil
	})
	if _, err := model.Call(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("ModelFunc did not invoke the adapted function")
	}
}

func TestRequestOwnsInputAndOptionsResolve(t *testing.T) {
	documents := []string{"first", "second"}
	request, requestErr := rerank.NewRequest("query", documents)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	documents[0] = "changed"
	if request.Documents[0] != "first" {
		t.Fatal("NewRequest aliases input documents")
	}

	var baseExtensions metadata.Extensions
	if err := baseExtensions.Set("provider/base", true); err != nil {
		t.Fatal(err)
	}
	base := rerank.Options{Model: "base", Extensions: baseExtensions}
	var overrideExtensions metadata.Extensions
	if err := overrideExtensions.Set("provider/request", "value"); err != nil {
		t.Fatal(err)
	}
	resolved, resolveErr := base.Resolve(rerank.Options{Model: "override", TopK: 1, Extensions: overrideExtensions})
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if resolved.Model != "override" || resolved.TopK != 1 || resolved.ResultLimit(2) != 1 {
		t.Fatalf("Resolve = %#v", resolved)
	}
	if _, ok, err := resolved.Extensions.Decode[bool]("provider/base"); err != nil || !ok {
		t.Fatalf("resolved base extension = %t, %v", ok, err)
	}
	if err := resolved.Extensions.Set("provider/base", false); err != nil {
		t.Fatal(err)
	}
	value, _, _ := base.Extensions.Decode[bool]("provider/base")
	if !value {
		t.Fatal("Resolve aliases base extensions")
	}
	if err := overrideExtensions.Set("provider/request", "changed"); err != nil {
		t.Fatal(err)
	}
	if value, ok, err := resolved.Extensions.Decode[string]("provider/request"); err != nil || !ok || value != "value" {
		t.Fatalf("Resolve aliases override extensions: %q, %t, %v", value, ok, err)
	}
	if got := (rerank.Options{}).ResultLimit(2); got != 2 {
		t.Fatalf("zero TopK limit = %d, want 2", got)
	}
}

func TestOptionsResolveValidatesTheEffectiveValue(t *testing.T) {
	for _, test := range []struct {
		name           string
		base, override rerank.Options
		want           rerank.Options
		invalid        bool
	}{
		{name: "preserve base", base: rerank.Options{Model: "base", TopK: 2}, want: rerank.Options{Model: "base", TopK: 2}},
		{name: "invalid base", base: rerank.Options{TopK: -1}, invalid: true},
		{name: "invalid override", base: rerank.Options{TopK: 2}, override: rerank.Options{TopK: -1}, invalid: true},
		{name: "invalid model", override: rerank.Options{Model: " model "}, invalid: true},
		{name: "override repairs base", base: rerank.Options{TopK: -1}, override: rerank.Options{TopK: 2}, want: rerank.Options{TopK: 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.base.Resolve(test.override)
			if test.invalid {
				if !errors.Is(err, rerank.ErrInvalidOptions) || !reflect.DeepEqual(got, rerank.Options{}) {
					t.Fatalf("Resolve = %+v, %v; want zero options and ErrInvalidOptions", got, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Resolve = %+v, %v; want %+v", got, err, test.want)
			}
		})
	}
}

func TestRequestValidation(t *testing.T) {
	tests := []struct {
		name    string
		request *rerank.Request
	}{
		{name: "nil"},
		{name: "blank query", request: &rerank.Request{Query: " ", Documents: []string{"document"}}},
		{name: "no documents", request: &rerank.Request{Query: "query"}},
		{name: "blank document", request: &rerank.Request{Query: "query", Documents: []string{" "}}},
		{name: "negative top K", request: &rerank.Request{Query: "query", Documents: []string{"document"}, Options: rerank.Options{TopK: -1}}},
		{name: "oversized top K", request: &rerank.Request{Query: "query", Documents: []string{"document"}, Options: rerank.Options{TopK: 2}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.request.Validate(); !errors.Is(err, rerank.ErrInvalidRequest) {
				t.Fatalf("Validate error = %v", err)
			}
		})
	}
	if err := (rerank.Options{Model: " model "}).Validate(); !errors.Is(err, rerank.ErrInvalidOptions) {
		t.Fatalf("Options.Validate error = %v", err)
	}
}

func TestResponseValidationForRequest(t *testing.T) {
	request, _ := rerank.NewRequest("query", []string{"first", "second", "third"})
	request.Options.TopK = 2
	response, err := rerank.NewResponse([]*rerank.Result{
		{Index: 2, Score: 0.9},
		{Index: 0, Score: 0.5},
	}, &rerank.ResponseMetadata{Model: "model", Usage: &rerank.Usage{InputTokens: 7}})
	if err != nil {
		t.Fatal(err)
	}
	if err := response.ValidateFor(request); err != nil {
		t.Fatal(err)
	}
	if response.First().Index != 2 {
		t.Fatalf("First index = %d, want 2", response.First().Index)
	}

	invalid := []struct {
		name    string
		results []*rerank.Result
	}{
		{name: "empty"},
		{name: "duplicate", results: []*rerank.Result{{Index: 0, Score: 0.9}, {Index: 0, Score: 0.8}}},
		{name: "ascending", results: []*rerank.Result{{Index: 0, Score: 0.5}, {Index: 1, Score: 0.8}}},
		{name: "non-finite", results: []*rerank.Result{{Index: 0, Score: rerank.Score(math.NaN())}}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := rerank.NewResponse(test.results, nil); !errors.Is(err, rerank.ErrInvalidResponse) {
				t.Fatalf("NewResponse error = %v", err)
			}
		})
	}
	if err := (&rerank.Response{Results: []*rerank.Result{{Index: 3, Score: 0.9}, {Index: 0, Score: 0.8}}}).ValidateFor(request); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("out-of-range ValidateFor error = %v", err)
	}
	if err := (&rerank.Response{Results: []*rerank.Result{{Index: 0, Score: 0.9}}}).ValidateFor(request); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("short ValidateFor error = %v", err)
	}
}

func TestJSONRoundTripAndTransactionalDecode(t *testing.T) {
	response := &rerank.Response{
		Results:  []*rerank.Result{{Index: 1, Score: 0.75}},
		Metadata: &rerank.ResponseMetadata{Model: "model", Usage: &rerank.Usage{InputTokens: 3}},
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded rerank.Response
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, *response) {
		t.Fatalf("round trip = %#v, want %#v", decoded, *response)
	}

	request := rerank.Request{Query: "keep", Documents: []string{"document"}}
	if err := json.Unmarshal([]byte(`{"query":" ","documents":[]}`), &request); !errors.Is(err, rerank.ErrInvalidRequest) {
		t.Fatalf("Unmarshal Request error = %v", err)
	}
	if request.Query != "keep" || len(request.Documents) != 1 {
		t.Fatalf("failed decode mutated receiver: %#v", request)
	}
	if _, err := json.Marshal(rerank.Usage{InputTokens: -1}); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("negative Usage marshal error = %v", err)
	}
}

func TestOptionsAndRequestJSONBoundaries(t *testing.T) {
	options := rerank.Options{Model: "model", TopK: 1}
	data, marshalErr := json.Marshal(options)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var decodedOptions rerank.Options
	if err := json.Unmarshal(data, &decodedOptions); err != nil {
		t.Fatal(err)
	}
	if decodedOptions.Model != options.Model || decodedOptions.TopK != options.TopK {
		t.Fatalf("options round trip = %#v", decodedOptions)
	}
	if _, err := json.Marshal(rerank.Options{TopK: -1}); !errors.Is(err, rerank.ErrInvalidOptions) {
		t.Fatalf("invalid options marshal error = %v", err)
	}
	if err := decodedOptions.UnmarshalJSON([]byte(`{`)); !errors.Is(err, rerank.ErrInvalidOptions) {
		t.Fatalf("malformed options error = %v", err)
	}
	var nilOptions *rerank.Options
	if err := nilOptions.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, rerank.ErrInvalidOptions) {
		t.Fatalf("nil options receiver error = %v", err)
	}

	request, requestErr := rerank.NewRequest("query", []string{"document"})
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	if _, err := json.Marshal(request); err != nil {
		t.Fatal(err)
	}
	if _, err := rerank.NewRequest("", []string{"document"}); !errors.Is(err, rerank.ErrInvalidRequest) {
		t.Fatalf("invalid NewRequest error = %v", err)
	}
	if _, err := json.Marshal(rerank.Request{}); !errors.Is(err, rerank.ErrInvalidRequest) {
		t.Fatalf("invalid request marshal error = %v", err)
	}
	if err := request.UnmarshalJSON([]byte(`{`)); !errors.Is(err, rerank.ErrInvalidRequest) {
		t.Fatalf("malformed request error = %v", err)
	}
	var nilRequest *rerank.Request
	if err := nilRequest.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, rerank.ErrInvalidRequest) {
		t.Fatalf("nil request receiver error = %v", err)
	}
}

func TestResponseValueJSONBoundaries(t *testing.T) {
	score := rerank.Score(0.75)
	if score.Float64() != 0.75 {
		t.Fatalf("Score.Float64 = %v", score.Float64())
	}
	data, marshalErr := json.Marshal(score)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var decodedScore rerank.Score
	if err := json.Unmarshal(data, &decodedScore); err != nil || decodedScore != score {
		t.Fatalf("score round trip = %v, %v", decodedScore, err)
	}
	if err := json.Unmarshal([]byte(`2`), &decodedScore); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("invalid score error = %v", err)
	}
	if err := decodedScore.UnmarshalJSON([]byte(`{`)); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("malformed score error = %v", err)
	}
	var nilScore *rerank.Score
	if err := nilScore.UnmarshalJSON([]byte(`0.5`)); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("nil score receiver error = %v", err)
	}

	result, resultErr := rerank.NewResult(1, score)
	if resultErr != nil {
		t.Fatal(resultErr)
	}
	data, marshalErr = json.Marshal(result)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var decodedResult rerank.Result
	if err := json.Unmarshal(data, &decodedResult); err != nil || decodedResult != *result {
		t.Fatalf("result round trip = %#v, %v", decodedResult, err)
	}
	if _, err := rerank.NewResult(-1, score); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("invalid NewResult error = %v", err)
	}
	var nilResult *rerank.Result
	if err := nilResult.Validate(); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("nil result validation error = %v", err)
	}
	if _, err := json.Marshal(rerank.Result{Index: -1}); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("invalid result marshal error = %v", err)
	}
	if err := decodedResult.UnmarshalJSON([]byte(`{`)); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("malformed result error = %v", err)
	}
	if err := nilResult.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("nil result receiver error = %v", err)
	}
}

func TestResponseMetadataJSONBoundaries(t *testing.T) {
	usage := rerank.Usage{InputTokens: 3}
	data, marshalErr := json.Marshal(usage)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var decodedUsage rerank.Usage
	if err := json.Unmarshal(data, &decodedUsage); err != nil || decodedUsage != usage {
		t.Fatalf("usage round trip = %#v, %v", decodedUsage, err)
	}
	if err := json.Unmarshal([]byte(`{"input_tokens":-1}`), &decodedUsage); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("invalid usage error = %v", err)
	}
	if err := decodedUsage.UnmarshalJSON([]byte(`{`)); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("malformed usage error = %v", err)
	}
	var nilUsage *rerank.Usage
	if err := nilUsage.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("nil usage receiver error = %v", err)
	}

	responseMetadata := rerank.ResponseMetadata{Model: "model", Usage: &usage}
	data, marshalErr = json.Marshal(responseMetadata)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var decodedMetadata rerank.ResponseMetadata
	if err := json.Unmarshal(data, &decodedMetadata); err != nil || decodedMetadata.Model != responseMetadata.Model {
		t.Fatalf("metadata round trip = %#v, %v", decodedMetadata, err)
	}
	if _, err := json.Marshal(rerank.ResponseMetadata{Model: " model "}); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("invalid metadata marshal error = %v", err)
	}
	if err := decodedMetadata.UnmarshalJSON([]byte(`{`)); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("malformed metadata error = %v", err)
	}
	var nilMetadata *rerank.ResponseMetadata
	if err := nilMetadata.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("nil metadata receiver error = %v", err)
	}
}

func TestResponseJSONRejectsInvalidReceiversAndValues(t *testing.T) {
	response := rerank.Response{Results: []*rerank.Result{{Index: 0, Score: 1}}}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded rerank.Response
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.First() == nil || decoded.First().Index != 0 {
		t.Fatalf("decoded response = %#v", decoded)
	}
	if (*rerank.Response)(nil).First() != nil || (&rerank.Response{}).First() != nil {
		t.Fatal("empty response returned a first result")
	}
	if _, err := json.Marshal(rerank.Response{}); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("invalid response marshal error = %v", err)
	}
	if err := decoded.UnmarshalJSON([]byte(`{`)); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("malformed response error = %v", err)
	}
	var nilResponse *rerank.Response
	if err := nilResponse.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("nil response receiver error = %v", err)
	}
	if err := (&rerank.Response{Results: []*rerank.Result{nil}}).Validate(); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("nil result response error = %v", err)
	}
	if err := (&rerank.Response{Results: []*rerank.Result{{Index: 0, Score: 1}}, Metadata: &rerank.ResponseMetadata{Model: " model "}}).Validate(); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("invalid response metadata error = %v", err)
	}
}
