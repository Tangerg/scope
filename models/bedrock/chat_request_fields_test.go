package bedrock_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/models/bedrock"
)

func TestChatRejectsNativeAliasesForCoreFactsBeforeIO(t *testing.T) {
	for _, test := range []struct {
		name    string
		native  string
		options corechat.Options
	}{
		{name: "temperature without Core value", native: `{"temperature":0.5}`},
		{name: "temperature matches Core", native: `{"temperature":0.5}`, options: corechat.Options{Temperature: new(0.5)}},
		{name: "temperature conflicts with Core", native: `{"temperature":0.5}`, options: corechat.Options{Temperature: new(0.25)}},
		{name: "null temperature", native: `{"temperature":null}`},
		{name: "Claude token limit", native: `{"max_tokens":128}`},
		{name: "Converse token limit", native: `{"maxTokens":128}`},
		{name: "Meta token limit", native: `{"max_gen_len":128}`},
		{name: "Titan inference configuration", native: `{"textGenerationConfig":{"maxTokenCount":128}}`},
		{name: "top p", native: `{"top_p":0.75}`},
		{name: "Cohere top p", native: `{"p":0.75}`},
		{name: "stop sequence", native: `{"stop_sequences":["END"]}`},
		{name: "Converse stop sequence", native: `{"stopSequences":["END"]}`},
		{name: "unsupported Core top k", native: `{"top_k":40}`},
		{name: "Cohere top k", native: `{"k":40}`},
		{name: "frequency penalty", native: `{"frequency_penalty":0.5}`},
		{name: "presence penalty", native: `{"presence_penalty":0.5}`},
		{name: "reasoning effort", native: `{"reasoning_effort":"high"}`},
		{name: "camel case reasoning effort", native: `{"reasoningEffort":"high"}`},
		{name: "output effort", native: `{"output_config":{"effort":"high"}}`},
		{name: "null output effort", native: `{"output_config":{"effort":null}}`},
		{name: "Nova reasoning effort", native: `{"reasoningConfig":{"type":"enabled","maxReasoningEffort":"high"}}`},
		{name: "null Nova reasoning effort", native: `{"reasoningConfig":{"maxReasoningEffort":null}}`},
		{name: "output format", native: `{"output_config":{"format":{"type":"json_schema"}}}`},
		{name: "model selection", native: `{"model":"another-model"}`},
		{name: "messages", native: `{"messages":[]}`},
		{name: "Cohere prompt", native: `{"preamble":"another system prompt"}`},
		{name: "tools", native: `{"tools":[]}`},
		{name: "tool choice", native: `{"tool_choice":"auto"}`},
	} {
		for _, fromDefaults := range []bool{false, true} {
			source := "request"
			if fromDefaults {
				source = "defaults"
			}
			t.Run(test.name+"/"+source, func(t *testing.T) {
				var calls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					writer.WriteHeader(http.StatusBadRequest)
				}))
				t.Cleanup(server.Close)
				defaults := corechat.Options{Model: "anthropic.claude-test"}
				request := newBedrockBehaviorRequest(t)
				request.Options = test.options.Clone()
				extensions := &request.Options.Extensions
				if fromDefaults {
					extensions = &defaults.Extensions
				}
				if err := extensions.Set(bedrock.ChatRequestExtensionKey, json.RawMessage(`{"additional_model_request_fields":`+test.native+`}`)); err != nil {
					t.Fatal(err)
				}
				model, err := bedrock.NewChat(t.Context(), bedrock.ChatConfig{
					Region: "us-east-1", BaseURL: server.URL, HTTPClient: server.Client(),
					Credentials: &bedrock.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, DefaultOptions: defaults,
				})
				if fromDefaults {
					if model != nil || err == nil || !strings.Contains(err.Error(), "owned by Core") || calls.Load() != 0 {
						t.Fatalf("model=%v error=%v provider calls=%d", model, err, calls.Load())
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), request)
				if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || !strings.Contains(err.Error(), "owned by Core") || calls.Load() != 0 {
					t.Fatalf("response=%v error=%v provider calls=%d", response, err, calls.Load())
				}
			})
		}
	}
}

func TestChatKeepsNativeOnlyFieldsSeparateFromCoreProjection(t *testing.T) {
	for _, nativeJSON := range []string{
		`{"thinking":{"type":"enabled","budget_tokens":1024}}`,
		`{"reasoning_config":{"type":"enabled","budget_tokens":1024}}`,
		`{"reasoningConfig":{"type":"disabled"}}`,
		`{"provider_data":{"temperature":0.9,"output_config":{"effort":"opaque"},"reasoningConfig":{"maxReasoningEffort":"opaque"}},"native_number":9007199254740993}`,
		`{"output_config":{"native_control":true}}`,
	} {
		t.Run(nativeJSON, func(t *testing.T) {
			type wireRequest struct {
				InferenceConfig struct {
					Temperature   float64  `json:"temperature"`
					TopP          float64  `json:"topP"`
					MaxTokens     int64    `json:"maxTokens"`
					StopSequences []string `json:"stopSequences"`
				} `json:"inferenceConfig"`
				AdditionalModelRequestFields metadata.Map `json:"additionalModelRequestFields"`
			}
			captured := make(chan wireRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var body wireRequest
				if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
					t.Error(err)
					http.Error(writer, "invalid request", http.StatusBadRequest)
					return
				}
				captured <- body
				writer.Header().Set("Content-Type", bedrockEventStreamContentType)
				writeBedrockEvent(writer, "messageStart", []byte(`{"role":"assistant"}`))
				writeBedrockEvent(writer, "contentBlockDelta", []byte(`{"contentBlockIndex":0,"delta":{"text":"answer"}}`))
				writeBedrockEvent(writer, "contentBlockStop", []byte(`{"contentBlockIndex":0}`))
				writeBedrockEvent(writer, "messageStop", []byte(`{"stopReason":"end_turn"}`))
			}))
			t.Cleanup(server.Close)
			request := newBedrockBehaviorRequest(t)
			request.Options = corechat.Options{Temperature: new(0.25), TopP: new(0.75), MaxOutputTokens: new(int64(64)), Stop: []string{"END"}}
			var native metadata.Map
			if err := jsonv2.Unmarshal([]byte(nativeJSON), &native); err != nil {
				t.Fatal(err)
			}
			if err := request.Options.Extensions.Set(bedrock.ChatRequestExtensionKey, bedrock.ChatRequestOptions{AdditionalModelRequestFields: native}); err != nil {
				t.Fatal(err)
			}
			if _, err := newBedrockBehaviorChat(t, server.URL).Call(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			body := <-captured
			inference := body.InferenceConfig
			if inference.Temperature != 0.25 || inference.TopP != 0.75 || inference.MaxTokens != 64 || !reflect.DeepEqual(inference.StopSequences, []string{"END"}) {
				t.Fatalf("Core inference projection=%+v", inference)
			}
			got, err := body.AdditionalModelRequestFields.Values()
			if err != nil {
				t.Fatal(err)
			}
			want, err := native.Values()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("native fields=%v, want %v", got, want)
			}
		})
	}
}

func TestChatNativeDefaultsDoNotRequireModelSelection(t *testing.T) {
	for _, nativeJSON := range []string{
		`{"thinking":{"type":"enabled","budget_tokens":1024}}`,
		`{"reasoning_config":{"type":"enabled","budget_tokens":1024}}`,
		`{"reasoningConfig":{"type":"disabled"}}`,
	} {
		t.Run(nativeJSON, func(t *testing.T) {
			var config bedrock.ChatConfig
			if err := config.DefaultOptions.Extensions.Set(bedrock.ChatRequestExtensionKey, json.RawMessage(`{"additional_model_request_fields":`+nativeJSON+`}`)); err != nil {
				t.Fatal(err)
			}
			if err := config.Validate(); err != nil {
				t.Fatalf("native-only defaults require a model: %v", err)
			}
		})
	}
}
