package protocol

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
)

// EmbeddingModelConfig binds provider access and defaults shared by every embedding call.
type EmbeddingModelConfig struct {
	Provider       string
	Client         ClientConfig
	DefaultOptions embedding.Options
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

// EmbeddingModel wraps Gemini's embed_content endpoint. New integrations use
// gemini-embedding-2, whose output dimensionality is configurable from 128 to
// 3072. Core's text-only request intentionally exposes only that model's text
// input capability; richer multimodal embedding inputs belong in a dedicated
// protocol rather than being hidden inside text.
type EmbeddingModel struct {
	api            *api
	provider       string
	defaultOptions embedding.Options
}

// NewEmbeddingModel rejects an invalid provider binding before the first embedding call.
func NewEmbeddingModel(config EmbeddingModelConfig) (*EmbeddingModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(config.Client)
	if err != nil {
		return nil, err
	}

	return &EmbeddingModel{
		api:            api,
		provider:       config.Provider,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (e *EmbeddingModel) buildAPIRequest(req *embedding.Request) (string, []*genai.Content, *genai.EmbedContentConfig, error) {
	effectiveOptions, err := e.defaultOptions.Resolve(req.Options)
	if err != nil {
		return "", nil, nil, err
	}

	cfgValue, _, err := effectiveOptions.Extensions.Decode[genai.EmbedContentConfig](protocolKey(e.provider, "embedding_request"))

	config := &cfgValue
	if err != nil {
		return "", nil, nil, err
	}

	if effectiveOptions.Dimensions != nil {
		if *effectiveOptions.Dimensions < 128 || *effectiveOptions.Dimensions > 3072 {
			return "", nil, nil, fmt.Errorf("google: embedding: dimensions must be between 128 and 3072: %d", *effectiveOptions.Dimensions)
		}
		config.OutputDimensionality = new(int32(*effectiveOptions.Dimensions))
	}

	contents := make([]*genai.Content, 0, len(req.Texts))
	for _, text := range req.Texts {
		contents = append(contents, genai.NewContentFromText(text, genai.RoleUser))
	}

	return effectiveOptions.Model, contents, config, nil
}

// buildResponse assembles one output per input text. embed_content answers in
// input order without tagging each embedding, so arrival order is the position,
// and sizing the slice from the request is what turns every mismatch into a
// local error: an extra embedding has nowhere to go, and an input the provider
// never answered stays nil and fails when the Response is built, naming the
// text that went unanswered.
func (e *EmbeddingModel) buildResponse(modelName string, apiResp *genai.EmbedContentResponse, expectedResults int) (*embedding.Response, error) {
	outputs := make([]*embedding.Output, expectedResults)
	for index, item := range apiResp.Embeddings {
		values := make([]float64, len(item.Values))
		for i, value := range item.Values {
			values[i] = float64(value)
		}

		var outputMetadata metadata.Map
		if item.Statistics != nil {
			if err := outputMetadata.Set(protocolKey(e.provider, "token_count"), item.Statistics.TokenCount); err != nil {
				return nil, err
			}
			if err := outputMetadata.Set(protocolKey(e.provider, "truncated"), item.Statistics.Truncated); err != nil {
				return nil, err
			}
		}

		if err := embedding.PlaceOutput(outputs, index, values, outputMetadata); err != nil {
			return nil, fmt.Errorf("google: embed_content response: %w", err)
		}
	}

	meta := &embedding.ResponseMetadata{
		Model: modelName,
	}
	if err := meta.Extra.Set(protocolKey(e.provider, "embedding_response"), apiResp); err != nil {
		return nil, err
	}
	if apiResp.Metadata != nil {
		// Gemini does not report per-modality prompt tokens; surface the
		// billable character count instead so callers can still cost the
		// call.
		if err := meta.Extra.Set(protocolKey(e.provider, "billable_character_count"), apiResp.Metadata.BillableCharacterCount); err != nil {
			return nil, err
		}
	}

	return embedding.NewResponse(outputs, meta)
}

func (e *EmbeddingModel) Call(ctx context.Context, req *embedding.Request) (response *embedding.Response, err error) {
	if err = req.Validate(); err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	modelName, contents, config, err := e.buildAPIRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := e.api.embedding(ctx, modelName, contents, config)
	if err != nil {
		return nil, err
	}

	return e.buildResponse(modelName, apiResp, len(req.Texts))
}
