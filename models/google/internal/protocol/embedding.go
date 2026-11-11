package protocol

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/metadata"
)

type EmbeddingModelConfig struct {
	Provider       string
	Client         ClientConfig
	DefaultOptions embedding.Options
}

// HTTP envelopes cannot be embedded in model options: SDK ExtraBody merging
// would let a native extension replace Core's texts, model, and dimensions.
type embeddingRequestOptions struct {
	TaskType             string `json:"taskType,omitempty"`
	Title                string `json:"title,omitempty"`
	MIMEType             string `json:"mimeType,omitempty"`
	AutoTruncate         *bool  `json:"autoTruncate,omitzero"`
	DocumentOCR          *bool  `json:"documentOcr,omitzero"`
	AudioTrackExtraction *bool  `json:"audioTrackExtraction,omitzero"`
}

func (e EmbeddingModelConfig) Validate() error {
	if err := validateProvider(e.Provider); err != nil {
		return fmt.Errorf("google: Provider: %w", err)
	}
	if err := e.Client.Validate(); err != nil {
		return err
	}
	if e.DefaultOptions.Model == "" {
		return errors.New("google: DefaultOptions.Model is required")
	}
	if err := e.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ embedding.Model = (*EmbeddingModel)(nil)

// EmbeddingModel admits the SDK's dimensionality representation; the selected
// model owns its supported dimensions. Core's text-only request cannot carry
// multimodal embedding inputs through a hidden native extension.
type EmbeddingModel struct {
	api            *api
	provider       string
	defaultOptions embedding.Options
}

func NewEmbeddingModel(ctx context.Context, config EmbeddingModelConfig) (*EmbeddingModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, config.Client)
	if err != nil {
		return nil, err
	}

	return &EmbeddingModel{
		api:            api,
		provider:       config.Provider,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (e *EmbeddingModel) buildAPIRequest(req *embedding.Request) (*embeddingRequest, error) {
	effectiveOptions := req.Options

	extensionKey := protocolKey(e.provider, EmbeddingRequestExtensionSuffix)
	raw, found, err := effectiveOptions.Extensions.Decode[json.RawMessage](extensionKey)
	if err != nil {
		return nil, fmt.Errorf("google: extension %q: %w", extensionKey, err)
	}
	nativeOptions := &embeddingRequestOptions{}
	if found {
		if err := jsonv2.Unmarshal(raw, &nativeOptions, jsonv2.RejectUnknownMembers(true)); err != nil {
			return nil, fmt.Errorf("google: extension %q: %w", extensionKey, err)
		}
		if nativeOptions == nil {
			return nil, fmt.Errorf("google: extension %q must be an object", extensionKey)
		}
	}
	config := &genai.EmbedContentConfig{
		TaskType:             nativeOptions.TaskType,
		Title:                nativeOptions.Title,
		MIMEType:             nativeOptions.MIMEType,
		DocumentOcr:          nativeOptions.DocumentOCR,
		AudioTrackExtraction: nativeOptions.AudioTrackExtraction,
	}

	if effectiveOptions.Dimensions != nil {
		if *effectiveOptions.Dimensions > math.MaxInt32 {
			return nil, fmt.Errorf("google: embedding: dimensions must fit int32, got %d", *effectiveOptions.Dimensions)
		}
		config.OutputDimensionality = new(int32(*effectiveOptions.Dimensions))
	}

	contents := make([]*genai.Content, 0, len(req.Texts))
	for _, text := range req.Texts {
		contents = append(contents, genai.NewContentFromText(text, genai.RoleUser))
	}

	return &embeddingRequest{model: effectiveOptions.Model, contents: contents, config: config, autoTruncate: nativeOptions.AutoTruncate}, nil
}

// buildResponse assembles one output per input text. embed_content answers in
// input order without tagging each embedding, so arrival order is the position,
// and sizing the slice from the request is what turns every mismatch into a
// local error: an extra embedding has nowhere to go, and an input the provider
// never answered stays nil and fails when the Response is built, naming the
// text that went unanswered.
func (e *EmbeddingModel) buildResponse(modelName string, apiResp *nativeEmbeddingResponse, req *embedding.Request) (*embedding.Response, error) {
	if apiResp == nil {
		return nil, fmt.Errorf("%w: google: missing embedding response", embedding.ErrInvalidResponse)
	}
	outputs := make([]*embedding.Output, len(req.Texts))
	for index, item := range apiResp.Embeddings {
		if item == nil {
			return nil, fmt.Errorf("%w: google: embeddings[%d] is null", embedding.ErrInvalidResponse, index)
		}

		var outputMetadata metadata.Map
		if presentJSON(item.TokenCount) {
			if err := outputMetadata.Set(protocolKey(e.provider, embeddingTokenCountMetadataSuffix), item.TokenCount); err != nil {
				return nil, err
			}
		}
		if presentJSON(item.Truncated) {
			if err := outputMetadata.Set(protocolKey(e.provider, embeddingTruncatedMetadataSuffix), item.Truncated); err != nil {
				return nil, err
			}
		}

		if err := embedding.PlaceOutput(outputs, index, item.Values, outputMetadata); err != nil {
			return nil, fmt.Errorf("google: embed_content response: %w", err)
		}
	}

	meta := &embedding.ResponseMetadata{
		Model: modelName,
	}
	if err := meta.Extra.Set(protocolKey(e.provider, EmbeddingResponseExtensionSuffix), apiResp.Raw); err != nil {
		return nil, err
	}
	if presentJSON(apiResp.BillableCharacterCount) {
		if err := meta.Extra.Set(protocolKey(e.provider, embeddingBillableCharacterCountMetadataSuffix), apiResp.BillableCharacterCount); err != nil {
			return nil, err
		}
	}

	out := &embedding.Response{Outputs: outputs, Metadata: meta}
	if err := out.ValidateFor(req); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *EmbeddingModel) Call(ctx context.Context, req *embedding.Request) (*embedding.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	options, err := e.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	effective := *req
	effective.Options = options
	req = &effective

	apiRequest, err := e.buildAPIRequest(req)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}

	apiResp, err := e.api.embedding(ctx, apiRequest)
	if err != nil {
		return nil, err
	}

	return e.buildResponse(apiRequest.model, apiResp, req)
}
