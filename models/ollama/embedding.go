package ollama

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/embedding"
)

// EmbeddingModelConfig binds provider access and defaults shared by every embedding call.
type EmbeddingModelConfig struct {
	DefaultOptions embedding.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (e EmbeddingModelConfig) Validate() error {
	if e.DefaultOptions.Model == "" {
		return errors.New("ollama: DefaultOptions.Model is required")
	}
	if err := e.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ embedding.Model = (*EmbeddingModel)(nil)

// EmbeddingModel wraps Ollama's /api/embed endpoint. Works with any
// embedding model the daemon has pulled: nomic-embed-text, mxbai-embed-large,
// snowflake-arctic-embed, etc. Use `ollama pull <model>` ahead of time.
type EmbeddingModel struct {
	api            *api
	defaultOptions embedding.Options
}

// NewEmbeddingModel rejects an invalid provider binding before the first embedding call.
func NewEmbeddingModel(config EmbeddingModelConfig) (*EmbeddingModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(apiConfig{
		BaseURL:    config.BaseURL,
		HTTPClient: config.HTTPClient,
	})
	if err != nil {
		return nil, err
	}

	return &EmbeddingModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (e *EmbeddingModel) buildAPIRequest(req *embedding.Request) (*nativeEmbedRequest, error) {
	effectiveOptions, err := e.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}

	apiRequest, _, err := effectiveOptions.Extensions.Decode[nativeEmbedRequest](EmbeddingRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	apiReq := &apiRequest
	apiReq.Model = effectiveOptions.Model
	apiReq.Input = req.Texts

	if effectiveOptions.Dimensions != nil {
		dimensions := int(*effectiveOptions.Dimensions)
		if int64(dimensions) != *effectiveOptions.Dimensions {
			return nil, fmt.Errorf("ollama: embedding: dimensions: %d exceeds int", *effectiveOptions.Dimensions)
		}
		apiReq.Dimensions = dimensions
	}

	return apiReq, nil
}

// buildResponse assembles one output per input text. This provider answers in
// input order without tagging each embedding, so arrival order is the position,
// and sizing the slice from the request is what turns every mismatch into a
// local error: an extra embedding has nowhere to go, and an input the provider
// never answered stays nil and fails when the Response is built, naming the
// text that went unanswered.
func (e *EmbeddingModel) buildResponse(apiResp *nativeEmbedResponse, expectedResults int) (*embedding.Response, error) {
	outputs := make([]*embedding.Output, expectedResults)
	for index, vec := range apiResp.Embeddings {
		values := make([]float64, len(vec))
		for i, value := range vec {
			values[i] = float64(value)
		}
		if err := embedding.PlaceOutput(outputs, index, values, nil); err != nil {
			return nil, fmt.Errorf("ollama: embed response: %w", err)
		}
	}

	meta := &embedding.ResponseMetadata{
		Model: apiResp.Model,
	}
	if err := meta.Extra.Set("ollama/total_duration_ns", apiResp.TotalDuration.Nanoseconds()); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set("ollama/load_duration_ns", apiResp.LoadDuration.Nanoseconds()); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set("ollama/prompt_eval_count", apiResp.PromptEvalCount); err != nil {
		return nil, err
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

	apiReq, err := e.buildAPIRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := e.api.embed(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	return e.buildResponse(apiResp, len(req.Texts))
}
