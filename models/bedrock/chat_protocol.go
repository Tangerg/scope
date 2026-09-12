package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"net/http"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

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

// converseAPI is the Converse surface Chat uses.
//
// Naming the two calls it makes keeps the chat path off the rest of the runtime
// client — invokeModel belongs to the embedding path and Chat never touches it
// — and lets the shared Model and Streamer suites drive this adapter's own
// mapping without an AWS endpoint. The event stream reader the SDK exposes for
// exactly that purpose supplies the streaming half.
type converseAPI interface {
	converse(
		ctx context.Context,
		params *bedrockruntime.ConverseInput,
		opts ...func(*bedrockruntime.Options),
	) (*bedrockruntime.ConverseOutput, error)
	converseStream(
		ctx context.Context,
		params *bedrockruntime.ConverseStreamInput,
		opts ...func(*bedrockruntime.Options),
	) (*bedrockruntime.ConverseStreamEventStream, error)
}

// Chat implements Core chat through Bedrock's provider-neutral Converse API.
type Chat struct {
	api      converseAPI
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
		stream, err := c.api.converseStream(ctx, input)
		if err != nil {
			yield(nil, err)
			return
		}
		defer stream.Close()

		// The last delta is held back so the finish reason can be stamped on
		// it. ConverseStream sends its usage-carrying metadata event after
		// messageStop, so reporting the end at messageStop put a delta after a
		// finished stream — which a [corechat.ResponseAccumulator] rejects.
		state := newProtocolChunkAccumulator(model)
		var terminal *corechat.ResponseDelta
		for event := range stream.Events() {
			response, include, mapErr := state.add(event)
			if mapErr != nil {
				yield(nil, mapErr)
				return
			}
			if !include {
				continue
			}
			if terminal != nil {
				if !yield(terminal, nil) {
					return
				}
				terminal = response
				continue
			}
			if state.terminated() {
				terminal = response
				continue
			}
			if !yield(response, nil) {
				return
			}
		}
		if streamErr := stream.Err(); streamErr != nil {
			yield(nil, streamErr)
			return
		}
		terminal, err = state.complete(terminal)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(terminal, nil)
	}
}

func (c *Chat) buildConverseInput(req *corechat.Request) (*bedrockruntime.ConverseInput, string, error) {
	prepared, err := c.prepareRequest(req)
	if err != nil {
		return nil, "", err
	}
	return &bedrockruntime.ConverseInput{
		ModelId:                           aws.String(prepared.model),
		AdditionalModelRequestFields:      toBedrockDocument(prepared.native.AdditionalModelRequestFields),
		AdditionalModelResponseFieldPaths: slices.Clone(prepared.native.AdditionalModelResponseFieldPaths),
		GuardrailConfig:                   mapGuardrailOptions(prepared.native.Guardrail),
		InferenceConfig:                   prepared.inference,
		Messages:                          prepared.messages,
		OutputConfig:                      mapOutputFormat(prepared.outputFormat),
		PerformanceConfig:                 mapPerformanceOptions(prepared.native.PerformanceLatency),
		RequestMetadata:                   maps.Clone(prepared.native.RequestMetadata),
		ServiceTier:                       mapServiceTier(prepared.native.ServiceTier),
		System:                            prepared.system,
		ToolConfig:                        prepared.tools,
	}, prepared.model, nil
}

func (c *Chat) buildConverseStreamInput(req *corechat.Request) (*bedrockruntime.ConverseStreamInput, string, error) {
	prepared, err := c.prepareRequest(req)
	if err != nil {
		return nil, "", err
	}
	return &bedrockruntime.ConverseStreamInput{
		ModelId:                           aws.String(prepared.model),
		AdditionalModelRequestFields:      toBedrockDocument(prepared.native.AdditionalModelRequestFields),
		AdditionalModelResponseFieldPaths: slices.Clone(prepared.native.AdditionalModelResponseFieldPaths),
		GuardrailConfig:                   mapStreamGuardrailOptions(prepared.native.StreamGuardrail),
		InferenceConfig:                   prepared.inference,
		Messages:                          prepared.messages,
		OutputConfig:                      mapOutputFormat(prepared.outputFormat),
		PerformanceConfig:                 mapPerformanceOptions(prepared.native.PerformanceLatency),
		RequestMetadata:                   maps.Clone(prepared.native.RequestMetadata),
		ServiceTier:                       mapServiceTier(prepared.native.ServiceTier),
		System:                            prepared.system,
		ToolConfig:                        prepared.tools,
	}, prepared.model, nil
}

func (c *Chat) prepareRequest(req *corechat.Request) (*preparedChatRequest, error) {
	if c == nil || c.api == nil {
		return nil, errors.New("bedrock: nil Chat")
	}
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("bedrock: request: %w", err)
	}
	options, err := c.defaults.Resolve(req.Options)
	if err != nil {
		return nil, fmt.Errorf("bedrock: options: %w", err)
	}
	if options.Model == "" {
		return nil, errors.New("bedrock: model is required in defaults or request options")
	}
	if options.FrequencyPenalty != nil || options.PresencePenalty != nil || options.TopK != nil {
		return nil, errors.New("bedrock: frequency_penalty, presence_penalty, and top_k are not supported by Converse inference configuration")
	}
	// Converse has no reasoning field. Reasoning rides in
	// additionalModelRequestFields, and its shape belongs to the model
	// generation rather than to Converse: Claude 3.7 takes
	// reasoning_config with a budget_tokens count, while the newer models
	// reject that form and take thinking with an output_config effort. Turning
	// an effort into a token budget would mean inventing the number, and
	// picking between the two shapes would mean guessing the generation from a
	// model id — so the option is refused and the caller states what the model
	// actually accepts through [ChatRequestOptions.AdditionalModelRequestFields].
	if options.ReasoningEffort != "" {
		return nil, fmt.Errorf(
			"bedrock: options.reasoning_effort %q is not supported: Converse has no reasoning field, so set the model's own reasoning parameters through the %q extension's AdditionalModelRequestFields",
			options.ReasoningEffort, ChatRequestExtensionKey)
	}

	native, found, err := req.Options.Extensions.Decode[ChatRequestOptions](ChatRequestExtensionKey)
	if err != nil {
		return nil, fmt.Errorf("bedrock: extension %q: %w", ChatRequestExtensionKey, err)
	}
	if !found {
		native = ChatRequestOptions{}
	} else {
		fields, _, decodeErr := req.Options.Extensions.Decode[map[string]json.RawMessage](ChatRequestExtensionKey)
		if decodeErr != nil {
			return nil, fmt.Errorf("bedrock: extension %q: %w", ChatRequestExtensionKey, decodeErr)
		}
		if _, exists := fields["json_schema"]; exists {
			return nil, fmt.Errorf("bedrock: extension %q field %q is owned by options.output_format", ChatRequestExtensionKey, "json_schema")
		}
	}

	system, messages, err := mapProtocolMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := mapProtocolTools(req.Tools, req.ToolChoice)
	if err != nil {
		return nil, err
	}
	if options.OutputFormat != nil && options.OutputFormat.Type == corechat.OutputFormatJSON {
		return nil, fmt.Errorf("%w: bedrock Converse does not support %q", corechat.ErrUnsupportedOutputFormat, options.OutputFormat.Type)
	}
	inference, err := mapInferenceOptions(options)
	if err != nil {
		return nil, err
	}
	return &preparedChatRequest{
		model:        options.Model,
		system:       system,
		messages:     messages,
		inference:    inference,
		tools:        tools,
		outputFormat: options.OutputFormat,
		native:       native,
	}, nil
}
