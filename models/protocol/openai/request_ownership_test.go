package openai_test

import (
	jsonv2 "encoding/json/v2"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func requestOwnershipBoundary(t *testing.T) (*httptest.Server, *modeltest.PollCounter) {
	t.Helper()
	calls := new(modeltest.PollCounter)
	server := modeltest.JSONServer(http.StatusBadRequest, `{"error":{"message":"unexpected provider call","type":"invalid_request_error"}}`, func(request *http.Request) {
		calls.Inc()
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		t.Logf("unexpected request %s: %s", request.URL.Path, body)
	})
	t.Cleanup(server.Close)
	return server, calls
}

func TestResponsesUnsupportedCoreOptionsCannotUseNativeFields(t *testing.T) {
	for _, field := range []struct {
		name  string
		value any
	}{
		{"frequency_penalty", 0},
		{"presence_penalty", 0},
		{"top_k", 40},
		{"stop", []string{}},
	} {
		for _, source := range []string{"request", "defaults", "dialect"} {
			for _, null := range []bool{false, true} {
				name := field.name + "/" + source
				if null {
					name += "/null"
				}
				t.Run(name, func(t *testing.T) {
					server, calls := requestOwnershipBoundary(t)
					value := field.value
					if null {
						value = nil
					}
					config := openai.ResponsesConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "model"}}
					binding := openai.ResponsesDialect{Provider: "openai"}
					request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("question"))}}
					switch source {
					case "request":
						if err := request.Options.Extensions.Set(openai.ResponsesRequestExtensionKey, openai.RequestFields{field.name: value}); err != nil {
							t.Fatal(err)
						}
					case "defaults":
						if err := config.DefaultOptions.Extensions.Set(openai.ResponsesRequestExtensionKey, openai.RequestFields{field.name: value}); err != nil {
							t.Fatal(err)
						}
					case "dialect":
						binding.PrepareRequest = func(_ *chat.Request, target *openai.CompatibleResponsesRequest) error {
							return target.SetExtraField(field.name, value)
						}
					}
					model, err := openai.NewResponses(t.Context(), config, binding)
					if err != nil {
						t.Fatal(err)
					}
					response, callErr := model.Call(t.Context(), request)
					count, countErr := model.CountInputTokens(t.Context(), request)
					if response != nil || invocation.ClassifyFailure(callErr) != invocation.FailureNotExecuted || count != 0 || countErr == nil || calls.N() != 0 {
						t.Fatalf("response=%v Call=%v count=%d CountInputTokens=%v requests=%d", response, callErr, count, countErr, calls.N())
					}
					if !strings.Contains(callErr.Error(), field.name) || !strings.Contains(countErr.Error(), field.name) {
						t.Fatalf("missing field diagnosis: Call=%v CountInputTokens=%v", callErr, countErr)
					}
				})
			}
		}
	}
}

func TestImageCoreOptionsCannotUseNativeFields(t *testing.T) {
	for _, field := range []struct {
		name  string
		value any
	}{
		{"seed", 0},
		{"negative_prompt", ""},
		{"width", 1024},
		{"height", 1024},
	} {
		for _, defaults := range []bool{false, true} {
			for _, null := range []bool{false, true} {
				name := field.name + "/request"
				if defaults {
					name = field.name + "/defaults"
				}
				if null {
					name += "/null"
				}
				t.Run(name, func(t *testing.T) {
					server, calls := requestOwnershipBoundary(t)
					value := field.value
					if null {
						value = nil
					}
					config := openai.ImageModelConfig{Provider: "openai", APIKey: "test", BaseURL: server.URL, DefaultOptions: image.Options{Model: "model"}}
					request := &image.Request{Prompt: "a square"}
					options := &request.Options
					if defaults {
						options = &config.DefaultOptions
					}
					if err := options.Extensions.Set(openai.ImageRequestExtensionKey, openai.RequestFields{field.name: value}); err != nil {
						t.Fatal(err)
					}
					model, err := openai.NewImageModel(t.Context(), config)
					if err != nil {
						t.Fatal(err)
					}
					response, err := model.Call(t.Context(), request)
					if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls.N() != 0 {
						t.Fatalf("response=%v error=%v requests=%d", response, err, calls.N())
					}
					if !strings.Contains(err.Error(), field.name) {
						t.Fatalf("missing field diagnosis: %v", err)
					}
				})
			}
		}
	}
}

func TestResponsesPreservesIndependentNativeControls(t *testing.T) {
	metadata := map[string]string{"top_k": "40", "stop": "application label"}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var wire struct {
			Metadata  map[string]string `json:"metadata"`
			Reasoning struct {
				Summary string `json:"summary"`
			} `json:"reasoning"`
			Text struct {
				Verbosity string `json:"verbosity"`
			} `json:"text"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &wire); err != nil {
			t.Error(err)
		}
		if !maps.Equal(wire.Metadata, metadata) || wire.Reasoning.Summary != "auto" || wire.Text.Verbosity != "low" {
			t.Errorf("native controls changed: %+v", wire)
		}
		writeResponsesCompletion(writer)
	}))
	t.Cleanup(server.Close)
	binding := openai.ResponsesDialect{
		Provider: "openai",
		PrepareRequest: func(_ *chat.Request, target *openai.CompatibleResponsesRequest) error {
			return target.SetExtraField("metadata", metadata)
		},
	}
	model, err := openai.NewResponses(t.Context(), openai.ResponsesConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "model"}}, binding)
	if err != nil {
		t.Fatal(err)
	}
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("question"))}}
	if err := request.Options.Extensions.Set(openai.ResponsesRequestExtensionKey, openai.RequestFields{
		"reasoning": map[string]any{"summary": "auto"},
		"text":      map[string]any{"verbosity": "low"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestImagePreservesNativeDeliveryAndCoreDimensions(t *testing.T) {
	server := modeltest.JSONServer(http.StatusOK, `{"data":[{"b64_json":"aW1hZ2U="}]}`, func(request *http.Request) {
		var wire struct {
			ResponseFormat string `json:"response_format"`
			Quality        string `json:"quality"`
			Size           string `json:"size"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &wire); err != nil {
			t.Error(err)
		}
		if wire.ResponseFormat != "b64_json" || wire.Quality != "hd" || wire.Size != "1024x1024" {
			t.Errorf("native controls or Core dimensions changed: %+v", wire)
		}
	})
	t.Cleanup(server.Close)
	model := newImageModel(t, server.URL, "dall-e-3")
	request := &image.Request{Prompt: "a square", Options: image.Options{Width: new(int64(1024)), Height: new(int64(1024))}}
	if err := request.Options.Extensions.Set(openai.ImageRequestExtensionKey, openai.RequestFields{"response_format": "b64_json", "quality": "hd"}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}
