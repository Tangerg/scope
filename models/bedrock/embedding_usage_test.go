package bedrock_test

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/bedrock"
)

func TestTitanBatchUsageRejectsOverflowBeforePublishing(t *testing.T) {
	for _, sample := range []struct {
		name    string
		counts  []*int64
		want    int64
		known   bool
		failure bool
		calls   int32
	}{
		{name: "exact boundary", counts: []*int64{new(int64(math.MaxInt64 - 1)), new(int64(1))}, want: math.MaxInt64, known: true, calls: 2},
		{name: "overflow", counts: []*int64{new(int64(math.MaxInt64)), new(int64(1))}, failure: true, calls: 2},
		{name: "wrap to zero", counts: []*int64{new(int64(math.MaxInt64)), new(int64(math.MaxInt64)), new(int64(2))}, failure: true, calls: 2},
		{name: "missing", counts: []*int64{new(int64(5)), nil, new(int64(7))}, calls: 3},
		{name: "negative after dispatch", counts: []*int64{new(int64(5)), new(int64(-1))}, failure: true, calls: 2},
	} {
		t.Run(sample.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				count := sample.counts[calls.Add(1)-1]
				w.Header().Set("Content-Type", "application/json")
				if count == nil {
					fmt.Fprint(w, `{"embedding":[1]}`)
					return
				}
				fmt.Fprintf(w, `{"embedding":[1],"inputTextTokenCount":%d}`, *count)
			}))
			defer server.Close()
			model, err := bedrock.NewEmbeddingModel(t.Context(), bedrock.EmbeddingModelConfig{Region: "us-east-1", BaseURL: server.URL, Credentials: &bedrock.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, DefaultOptions: embedding.Options{Model: "amazon.titan-embed-text-v2:0"}})
			if err != nil {
				t.Fatal(err)
			}
			texts := make([]string, len(sample.counts))
			for index := range texts {
				texts[index] = "text"
			}
			response, err := model.Call(t.Context(), &embedding.Request{Texts: texts})
			if calls.Load() != sample.calls {
				t.Fatalf("calls=%d", calls.Load())
			}
			if sample.failure {
				if response != nil || !errors.Is(err, embedding.ErrInvalidResponse) || invocation.ClassifyFailure(err) != invocation.FailureUnknown {
					t.Fatalf("response=%v error=%v", response, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			usage := response.Metadata.Usage
			if (usage != nil) != sample.known || usage != nil && usage.InputTokens != sample.want {
				t.Fatalf("usage=%+v", usage)
			}
		})
	}
}
