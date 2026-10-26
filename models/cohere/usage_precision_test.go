package cohere_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/models/cohere"
)

func cohereUsage(t *testing.T, modality, nativeUsage string) (*int64, metadata.Map, error) {
	t.Helper()
	body := `{"results":[{"index":0,"relevance_score":1}],"meta":` + nativeUsage + `}`
	if modality == "embedding" {
		body = `{"embeddings":{"float":[[1]]},"meta":` + nativeUsage + `}`
	}
	server := modeltest.JSONServer(http.StatusOK, body)
	t.Cleanup(server.Close)
	if modality == "embedding" {
		options := embedding.Options{Model: "embed-v4.0"}
		if err := options.Extensions.Set(cohere.EmbeddingRequestExtensionKey, cohere.EmbeddingRequestOptions{InputType: "search_document"}); err != nil {
			t.Fatal(err)
		}
		model, err := cohere.NewEmbeddingModel(t.Context(), cohere.EmbeddingModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
		if err != nil {
			t.Fatal(err)
		}
		response, err := model.Call(t.Context(), &embedding.Request{Texts: []string{"input"}})
		if err != nil {
			return nil, nil, err
		}
		if response.Metadata.Usage == nil {
			return nil, response.Metadata.Extra, nil
		}
		return new(response.Metadata.Usage.InputTokens), response.Metadata.Extra, nil
	}
	model, err := cohere.NewRerankModel(t.Context(), cohere.RerankModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: rerank.Options{Model: cohere.ModelRerankV35}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), &rerank.Request{Query: "query", Documents: []string{"document"}})
	if err != nil {
		return nil, nil, err
	}
	if response.Metadata.Usage == nil {
		return nil, response.Metadata.Extra, nil
	}
	return new(response.Metadata.Usage.InputTokens), response.Metadata.Extra, nil
}

func TestTokenUsagePreservesExactPhysicalAndBilledFacts(t *testing.T) {
	for _, modality := range []string{"embedding", "rerank"} {
		for _, test := range []struct {
			name, raw        string
			physical, billed *int64
		}{
			{"absent", `{}`, nil, nil},
			{"null", `{"tokens":{"input_tokens":null},"billed_units":{"input_tokens":null}}`, nil, nil},
			{"zero", `{"tokens":{"input_tokens":0},"billed_units":{"input_tokens":0}}`, new(int64(0)), new(int64(0))},
			{"exact large", `{"tokens":{"input_tokens":9007199254740993},"billed_units":{"input_tokens":9007199254740991}}`, new(int64(9007199254740993)), new(int64(9007199254740991))},
			{"maximum", `{"tokens":{"input_tokens":9223372036854775807}}`, new(int64(math.MaxInt64)), nil},
			{"decimal integer", `{"tokens":{"input_tokens":1.0}}`, new(int64(1)), nil},
			{"exponent integer", `{"tokens":{"input_tokens":1e3}}`, new(int64(1000)), nil},
			{"billed only", `{"billed_units":{"input_tokens":9007199254740993}}`, nil, new(int64(9007199254740993))},
		} {
			t.Run(modality+"/"+test.name, func(t *testing.T) {
				physical, extra, err := cohereUsage(t, modality, test.raw)
				if err != nil || (physical == nil) != (test.physical == nil) || physical != nil && *physical != *test.physical {
					t.Fatalf("physical=%v want=%v error=%v", physical, test.physical, err)
				}
				billed, present, err := extra.Decode[int64]("cohere/billed_input_tokens")
				if err != nil || present != (test.billed != nil) || present && billed != *test.billed {
					t.Fatalf("billed=%d present=%t want=%v error=%v", billed, present, test.billed, err)
				}
			})
		}
	}
}

func TestTokenUsageRejectsNonIntegralNegativeAndOverflowingCounts(t *testing.T) {
	for _, modality := range []string{"embedding", "rerank"} {
		for _, field := range []string{"tokens", "billed_units"} {
			for _, raw := range []string{"-1", "0.5", "9223372036854775808", "1e100", `"1"`} {
				t.Run(modality+"/"+field+"/"+raw, func(t *testing.T) {
					_, _, err := cohereUsage(t, modality, `{"`+field+`":{"input_tokens":`+raw+`}}`)
					failure := rerank.ErrInvalidResponse
					if modality == "embedding" {
						failure = embedding.ErrInvalidResponse
					}
					if !errors.Is(err, failure) {
						t.Fatalf("invalid token count admitted or misclassified: %v", err)
					}
				})
			}
		}
	}
}

func TestBilledSearchUnitsPreserveExactCounts(t *testing.T) {
	_, extra, err := cohereUsage(t, "rerank", `{"billed_units":{"search_units":9007199254740993}}`)
	if err != nil {
		t.Fatal(err)
	}
	units, present, err := extra.Decode[int64](cohere.RerankSearchUnitsMetadataKey)
	if err != nil || !present || units != 9007199254740993 {
		t.Fatalf("units=%d present=%t error=%v", units, present, err)
	}
	if _, _, err := cohereUsage(t, "rerank", `{"billed_units":{"search_units":0.5}}`); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("fractional search units admitted: %v", err)
	}
}

func TestUsageCaptureBelongsToOneCall(t *testing.T) {
	arrived, release := make(chan struct{}, 2), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Texts []string `json:"texts"`
		}
		if err := jsonv2.UnmarshalRead(r.Body, &request); err != nil {
			t.Error(err)
			return
		}
		arrived <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"embeddings":{"float":[[1]]},"meta":{"tokens":{"input_tokens":%s}}}`, request.Texts[0])
	}))
	defer server.Close()
	options := embedding.Options{Model: "embed-v4.0"}
	if err := options.Extensions.Set(cohere.EmbeddingRequestExtensionKey, cohere.EmbeddingRequestOptions{InputType: "search_document"}); err != nil {
		t.Fatal(err)
	}
	model, err := cohere.NewEmbeddingModel(t.Context(), cohere.EmbeddingModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
	if err != nil {
		t.Fatal(err)
	}
	var calls sync.WaitGroup
	for _, count := range []int64{9007199254740993, 42} {
		calls.Go(func() {
			response, err := model.Call(t.Context(), &embedding.Request{Texts: []string{strconv.FormatInt(count, 10)}})
			if err != nil || response == nil || response.Metadata.Usage == nil || response.Metadata.Usage.InputTokens != count {
				t.Errorf("call count=%d response=%v error=%v", count, response, err)
			}
		})
	}
	<-arrived
	<-arrived
	close(release)
	calls.Wait()
}
