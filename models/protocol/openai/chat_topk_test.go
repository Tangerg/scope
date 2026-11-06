package openai_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestChatTopKHasOneCoreOwner(t *testing.T) {
	for _, test := range []struct {
		name            string
		supported       bool
		defaultTopK     *int64
		requestTopK     *int64
		extension       string
		dialectOverride bool
		want            *int64
		wantErr         string
	}{
		{name: "absent stays absent"},
		{name: "supported default", supported: true, defaultTopK: new(int64(40)), want: new(int64(40))},
		{name: "supported request", supported: true, requestTopK: new(int64(7)), want: new(int64(7))},
		{name: "request overrides default", supported: true, defaultTopK: new(int64(40)), requestTopK: new(int64(7)), want: new(int64(7))},
		{name: "unsupported dialect", requestTopK: new(int64(40)), wantErr: "not supported"},
		{name: "invalid Core value", supported: true, requestTopK: new(int64(-1)), wantErr: "top_k must be greater than zero"},
		{name: "raw field without Core", supported: true, extension: `{"top_k":40}`, wantErr: "owned by Core"},
		{name: "raw same value", supported: true, requestTopK: new(int64(40)), extension: `{"top_k":40}`, wantErr: "owned by Core"},
		{name: "raw different value", supported: true, requestTopK: new(int64(40)), extension: `{"top_k":7}`, wantErr: "owned by Core"},
		{name: "raw invalid value", supported: true, extension: `{"top_k":-1}`, wantErr: "owned by Core"},
		{name: "raw on unsupported dialect", extension: `{"top_k":40}`, wantErr: "owned by Core"},
		{name: "callback cannot replace Core", supported: true, requestTopK: new(int64(40)), dialectOverride: true, wantErr: "owned by Core"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			captured := make(chan *int64, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				var body struct {
					TopK *int64 `json:"top_k"`
					Seed int64  `json:"seed"`
				}
				if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
					t.Error(err)
					http.Error(writer, "invalid request", http.StatusBadRequest)
					return
				}
				if body.Seed != 5 {
					t.Errorf("seed=%d, want independent dialect field", body.Seed)
				}
				captured <- body.TopK
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(writer, "data: {\"id\":\"chat\",\"model\":\"model\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"content\":\"answer\"}}]}\n\ndata: [DONE]\n\n")
			}))
			t.Cleanup(server.Close)
			dialect := openai.DialectConfig{
				Provider: "provider", TokenLimitField: openai.TokenLimitMaxTokens, SupportsTopK: test.supported,
				PrepareRequest: func(_ *chat.Request, target *openai.CompatibleRequest) error {
					if test.dialectOverride {
						return target.SetExtraField("top_k", 99)
					}
					return target.SetExtraField("seed", 5)
				},
			}
			model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test-key", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "model", TopK: test.defaultTopK}}, dialect)
			if err != nil {
				t.Fatal(err)
			}
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}, Options: chat.Options{TopK: test.requestTopK}}
			if test.extension != "" {
				if extensionErr := request.Options.Extensions.Set("provider"+openai.CompatibleRequestExtensionSuffix, json.RawMessage(test.extension)); extensionErr != nil {
					t.Fatal(extensionErr)
				}
			}
			response, err := model.Call(t.Context(), request)
			if test.wantErr != "" {
				if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || !strings.Contains(err.Error(), test.wantErr) || calls.Load() != 0 {
					t.Fatalf("response=%v error=%v provider calls=%d", response, err, calls.Load())
				}
				return
			}
			if err != nil || calls.Load() != 1 {
				t.Fatalf("response=%v error=%v provider calls=%d", response, err, calls.Load())
			}
			got := <-captured
			if (got == nil) != (test.want == nil) || got != nil && *got != *test.want {
				t.Fatalf("top_k=%v, want %v", got, test.want)
			}
		})
	}
}
