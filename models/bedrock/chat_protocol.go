package bedrock

import (
	"context"
	"fmt"
	"iter"
	"net/http"

	corechat "github.com/Tangerg/scope/core/chat"
)

const (
	// ChatRequestExtensionKey stores [ChatRequestOptions] in a Core request.
	ChatRequestExtensionKey = "bedrock/request"
	// ChatResponseExtensionKey preserves the complete official Converse output.
	ChatResponseExtensionKey  = "bedrock/response"
	chatReasoningKindKey      = "bedrock/reasoning_kind"
	chatReasoningText         = "reasoning_text"
	chatReasoningRedacted     = "redacted_content"
	chatNativeFinishReasonKey = "bedrock/native_finish_reason"
)

// ChatRequestOptions carries serializable Bedrock Converse fields that have no
// provider-neutral Core equivalent. Common model, message, tool, and sampling
// fields are always derived from the Core request and take precedence.
type ChatRequestOptions struct {
	AdditionalModelRequestFields      map[string]any          `json:"additional_model_request_fields,omitempty"`
	AdditionalModelResponseFieldPaths []string                `json:"additional_model_response_field_paths,omitempty"`
	Guardrail                         *GuardrailOptions       `json:"guardrail,omitempty"`
	StreamGuardrail                   *StreamGuardrailOptions `json:"stream_guardrail,omitempty"`
	PerformanceLatency                string                  `json:"performance_latency,omitempty"`
	RequestMetadata                   map[string]string       `json:"request_metadata,omitempty"`
	ServiceTier                       string                  `json:"service_tier,omitempty"`
}

// GuardrailOptions configures a Bedrock guardrail without exposing AWS SDK
// wire types.
type GuardrailOptions struct {
	Identifier string `json:"identifier"`
	Version    string `json:"version"`
	Trace      string `json:"trace,omitempty"`
}

// StreamGuardrailOptions adds the streaming processing mode to a guardrail.
type StreamGuardrailOptions struct {
	Identifier     string `json:"identifier"`
	Version        string `json:"version"`
	Trace          string `json:"trace,omitempty"`
	ProcessingMode string `json:"processing_mode,omitempty"`
}

// ChatConfig binds provider access and defaults shared by every chat call.
type ChatConfig struct {
	DefaultOptions corechat.Options
	Region         string
	BaseURL        string
	HTTPClient     *http.Client
	Credentials    *Credentials
}

func (c ChatConfig) Validate() error {
	if err := c.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("bedrock: DefaultOptions: %w", err)
	}
	return nil
}

var (
	_ corechat.Model    = (*Chat)(nil)
	_ corechat.Streamer = (*Chat)(nil)
)

// Chat implements Core chat through Bedrock's provider-neutral Converse API.
type Chat struct {
	api      *api
	defaults corechat.Options
}

// NewChat rejects an invalid provider binding before the first chat call.
func NewChat(ctx context.Context, config ChatConfig) (*Chat, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(ctx, apiConfig{
		Region:      config.Region,
		BaseURL:     config.BaseURL,
		HTTPClient:  config.HTTPClient,
		Credentials: config.Credentials,
	})
	if err != nil {
		return nil, err
	}
	return &Chat{api: api, defaults: config.DefaultOptions.Clone()}, nil
}

func (c *Chat) Call(ctx context.Context, req *corechat.Request) (*corechat.Response, error) {
	input, model, err := c.buildConverseInput(req)
	if err != nil {
		return nil, err
	}
	output, err := c.api.converse(ctx, input)
	if err != nil {
		return nil, err
	}
	return mapProtocolConverseResponse(model, output)
}

// Stream performs one Bedrock ConverseStream request and yields validated
// provider deltas with cumulative usage snapshots.
func (c *Chat) Stream(ctx context.Context, req *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
	return func(yield func(*corechat.ResponseDelta, error) bool) {
		input, model, err := c.buildConverseStreamInput(req)
		if err != nil {
			yield(nil, err)
			return
		}
		output, err := c.api.converseStream(ctx, input)
		if err != nil {
			yield(nil, err)
			return
		}
		stream := output.GetStream()
		defer stream.Close()

		state := newProtocolChunkAccumulator(model)
		for event := range stream.Events() {
			response, include, mapErr := state.add(event)
			if mapErr != nil {
				yield(nil, mapErr)
				return
			}
			if include && !yield(response, nil) {
				return
			}
		}
		if streamErr := stream.Err(); streamErr != nil {
			yield(nil, streamErr)
			return
		}
		if !state.terminated() {
			yield(nil, fmt.Errorf("bedrock: stream: %w: missing terminal response", corechat.ErrInvalidResponse))
		}
	}
}
