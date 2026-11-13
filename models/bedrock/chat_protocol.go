package bedrock

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"maps"
	"net/http"
	"slices"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/metadata"
)

const (
	// ChatRequestExtensionKey stores [ChatRequestOptions] in a Core request.
	ChatRequestExtensionKey = Provider + "/request"
	// ChatMessageStopExtensionKey preserves the terminal provider outcome and native fields.
	ChatMessageStopExtensionKey = Provider + "/message_stop"
	// ChatMetadataExtensionKey preserves provider usage, metrics, and trace data.
	ChatMetadataExtensionKey  = Provider + "/metadata"
	chatReasoningKindKey      = "bedrock/reasoning_kind"
	chatReasoningText         = "reasoning_text"
	chatReasoningRedacted     = "redacted_content"
	chatNativeFinishReasonKey = "bedrock/native_finish_reason"
)

// ChatRequestOptions carries serializable Bedrock Converse fields that have no
// provider-neutral Core equivalent. Common model, message, tool, and sampling
// fields are derived exclusively from the Core request.
type ChatRequestOptions struct {
	AdditionalModelRequestFields      metadata.Map      `json:"additional_model_request_fields,omitempty"`
	AdditionalModelResponseFieldPaths []string          `json:"additional_model_response_field_paths,omitempty"`
	Guardrail                         *GuardrailOptions `json:"guardrail,omitzero"`
	PerformanceLatency                string            `json:"performance_latency,omitempty"`
	RequestMetadata                   map[string]string `json:"request_metadata,omitempty"`
	ServiceTier                       string            `json:"service_tier,omitempty"`
}

func (c *ChatRequestOptions) UnmarshalJSON(data []byte) error {
	type wireOptions ChatRequestOptions
	var decoded *wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	if decoded == nil {
		return errors.New("bedrock: chat request options must be a JSON object")
	}
	if err := validateAdditionalModelRequestFields(decoded.AdditionalModelRequestFields); err != nil {
		return err
	}
	if decoded.Guardrail != nil && (decoded.Guardrail.Identifier == "" || decoded.Guardrail.Version == "") {
		return errors.New("bedrock: guardrail requires identifier and version")
	}
	*c = ChatRequestOptions(*decoded)
	return nil
}

type GuardrailOptions struct {
	Identifier     string `json:"identifier"`
	Version        string `json:"version"`
	Trace          string `json:"trace,omitempty"`
	ProcessingMode string `json:"processing_mode,omitempty"`
}

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
	if _, _, err := c.DefaultOptions.Extensions.Decode[ChatRequestOptions](ChatRequestExtensionKey); err != nil {
		return fmt.Errorf("bedrock: DefaultOptions: %w", err)
	}
	return nil
}

var (
	_ corechat.Model    = (*Chat)(nil)
	_ corechat.Streamer = (*Chat)(nil)
)

// converseAPI consumes only the streaming capability of the runtime client.
type converseAPI interface {
	converseStream(
		ctx context.Context,
		params *bedrockruntime.ConverseStreamInput,
		opts ...func(*bedrockruntime.Options),
	) (*bedrockruntime.ConverseStreamEventStream, error)
}

// Chat implements Core chat through Bedrock's provider-neutral Converse API.
// Responses support text, reasoning, and client tool calls. Native content
// without a Core mapping returns [errors.ErrUnsupported].
type Chat struct {
	api      converseAPI
	defaults corechat.Options
}

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
	return corechat.Collect(c.Stream(ctx, req))
}

// Stream performs one Bedrock ConverseStream request and yields validated
// provider deltas with cumulative usage snapshots. Invalid message or content
// block lifecycles return [corechat.ErrInvalidResponse], including clean transport
// EOF before messageStop. Termination follows all metadata and transport checks.
// Server tool use returns [errors.ErrUnsupported] before publishing a tool call.
func (c *Chat) Stream(ctx context.Context, req *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
	return func(yield func(*corechat.ResponseDelta, error) bool) {
		input, model, err := c.buildConverseStreamInput(req)
		if err != nil {
			yield(nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err})
			return
		}
		stream, err := c.api.converseStream(ctx, input)
		if err != nil {
			yield(nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err})
			return
		}
		closeStream := sync.OnceValue(stream.Close)
		defer func() { _ = closeStream() }()

		// The last delta is held back so the finish reason can be stamped on
		// it. ConverseStream sends its usage-carrying metadata event after
		// messageStop, so reporting the end at messageStop put a delta after a
		// finished stream — which a [corechat.ResponseAccumulator] rejects.
		state := newConverseStream(model)
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
		if streamErr := errors.Join(closeStream(), stream.Err(), ctx.Err()); streamErr != nil {
			yield(nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: streamErr})
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

func (c *Chat) buildConverseStreamInput(req *corechat.Request) (*bedrockruntime.ConverseStreamInput, string, error) {
	prepared, err := c.prepareRequest(req)
	if err != nil {
		return nil, "", err
	}
	input := &bedrockruntime.ConverseStreamInput{
		ModelId:                           aws.String(prepared.model),
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
	}
	if prepared.native.AdditionalModelRequestFields != nil {
		encoded, err := jsonv2.Marshal(prepared.native.AdditionalModelRequestFields)
		if err != nil {
			return nil, "", err
		}
		native, err := documentFromJSON(encoded)
		if err != nil {
			return nil, "", err
		}
		input.AdditionalModelRequestFields = native
	}
	return input, prepared.model, nil
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
	resolvedRequest := *req
	resolvedRequest.Options = options
	req = &resolvedRequest

	if options.Model == "" {
		return nil, errors.New("bedrock: model is required in defaults or request options")
	}
	if options.FrequencyPenalty != nil || options.PresencePenalty != nil || options.TopK != nil {
		return nil, errors.New("bedrock: frequency_penalty, presence_penalty, and top_k are not supported by Converse inference configuration")
	}
	// Converse has no portable reasoning control. Mapping an effort to a
	// model's token budget would invent a value and guess a model generation.
	if options.ReasoningEffort != "" {
		return nil, fmt.Errorf(
			"bedrock: options.reasoning_effort %q is not supported by this Converse binding",
			options.ReasoningEffort)
	}

	native, _, err := req.Options.Extensions.Decode[ChatRequestOptions](ChatRequestExtensionKey)
	if err != nil {
		return nil, fmt.Errorf("bedrock: extension %q: %w", ChatRequestExtensionKey, err)
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
	prepared := &preparedChatRequest{
		model:        options.Model,
		system:       system,
		messages:     messages,
		inference:    inference,
		tools:        tools,
		outputFormat: options.OutputFormat,
		native:       native,
	}
	if err := prepared.validate(); err != nil {
		return nil, err
	}
	return prepared, nil
}
