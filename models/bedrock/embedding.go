package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	"github.com/Tangerg/scope/core/embedding"
)

const (
	// EmbeddingRequestExtensionKey stores [EmbeddingRequestOptions] in Core
	// embedding options.
	EmbeddingRequestExtensionKey = "bedrock/embedding_request"
	// EmbeddingResponseExtensionKey preserves the provider's JSON response body.
	// Titan batching stores one body per input; Cohere stores its single batch body.
	EmbeddingResponseExtensionKey       = "bedrock/embedding_response"
	maximumCohereBatchTexts             = 96
	embeddingDimension256         int64 = 256
	embeddingDimension512         int64 = 512
	embeddingDimension1024        int64 = 1024
	embeddingDimension1536        int64 = 1536
)

// EmbeddingRequestOptions carries official family-specific InvokeModel fields
// with no provider-neutral Core equivalent. Unsupported fields are rejected for
// the selected model family rather than silently ignored.
type EmbeddingRequestOptions struct {
	// InputType is required by Cohere Embed and distinguishes retrieval queries,
	// retrieval documents, classification inputs, and clustering inputs.
	InputType string `json:"input_type,omitempty"`
	// Truncate controls Cohere's over-length behavior.
	Truncate string `json:"truncate,omitempty"`
	// Normalize controls Amazon Titan Text Embeddings V2 output normalization.
	Normalize *bool `json:"normalize,omitempty"`
}

// EmbeddingModelConfig binds provider access and defaults shared by every embedding call.
type EmbeddingModelConfig struct {
	DefaultOptions embedding.Options
	Region         string
	BaseURL        string
	HTTPClient     *http.Client
	Credentials    *Credentials
}

func (e EmbeddingModelConfig) Validate() error {
	if e.DefaultOptions.Model == "" {
		return errors.New("bedrock: DefaultOptions.Model is required")
	}
	if err := e.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ embedding.Model = (*EmbeddingModel)(nil)

// EmbeddingModel implements the native InvokeModel wire contracts for Amazon
// Titan Text Embeddings V1/V2 and Cohere Embed V3/V4. Titan accepts one input
// per invocation; Cohere accepts up to 96 texts in one batch.
type EmbeddingModel struct {
	api            *api
	defaultOptions embedding.Options
}

// NewEmbeddingModel rejects an invalid provider binding before the first embedding call.
func NewEmbeddingModel(ctx context.Context, config EmbeddingModelConfig) (*EmbeddingModel, error) {
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
	return &EmbeddingModel{api: api, defaultOptions: config.DefaultOptions.Clone()}, nil
}

func (e *EmbeddingModel) Call(ctx context.Context, req *embedding.Request) (response *embedding.Response, err error) {
	if err = req.Validate(); err != nil {
		return nil, err
	}
	// Titan embeds one text per invocation and Cohere embeds the batch in one,
	// so the request-wide correspondence is only whole once a family path has
	// finished. Each path still accounts for its own invocation; this asks the
	// question the caller actually cares about.
	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	effectiveOptions, err := e.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	nativeValue, _, err := effectiveOptions.Extensions.Decode[EmbeddingRequestOptions](EmbeddingRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	native := &nativeValue

	family, err := classifyEmbeddingModel(effectiveOptions.Model)
	if err != nil {
		return nil, err
	}

	var batch *embeddingBatch
	switch family {
	case embeddingFamilyTitanV1, embeddingFamilyTitanV2:
		batch, err = e.embedTitan(ctx, family, effectiveOptions.Model, req.Texts, effectiveOptions.Dimensions, native)
	case embeddingFamilyCohereV3, embeddingFamilyCohereV4:
		batch, err = e.embedCohere(ctx, family, effectiveOptions.Model, req.Texts, effectiveOptions.Dimensions, native)
	default:
		return nil, fmt.Errorf("bedrock: unsupported internal embedding family %d", family)
	}
	if err != nil {
		return nil, err
	}

	outputs := make([]*embedding.Output, len(batch.vectors))
	for index, vector := range batch.vectors {
		output, resultErr := embedding.NewOutput(vector, nil)
		if resultErr != nil {
			return nil, fmt.Errorf("bedrock: embedding response output %d: %w", index, resultErr)
		}
		outputs[index] = output
	}

	metadata := &embedding.ResponseMetadata{Model: effectiveOptions.Model}
	if batch.hasUsage {
		metadata.Usage = &embedding.Usage{InputTokens: batch.inputTokens}
	}
	if len(batch.responseBodies) == 1 {
		if err := metadata.Extra.Set(EmbeddingResponseExtensionKey, batch.responseBodies[0]); err != nil {
			return nil, err
		}
	} else if len(batch.responseBodies) > 1 {
		if err := metadata.Extra.Set(EmbeddingResponseExtensionKey, batch.responseBodies); err != nil {
			return nil, err
		}
	}
	return embedding.NewResponse(outputs, metadata)
}

type embeddingFamily uint8

const (
	embeddingFamilyTitanV1 embeddingFamily = iota + 1
	embeddingFamilyTitanV2
	embeddingFamilyCohereV3
	embeddingFamilyCohereV4
)

func classifyEmbeddingModel(modelID string) (embeddingFamily, error) {
	switch {
	case strings.Contains(modelID, "amazon.titan-embed-text-v1"):
		return embeddingFamilyTitanV1, nil
	case strings.Contains(modelID, "amazon.titan-embed-text-v2"):
		return embeddingFamilyTitanV2, nil
	case strings.Contains(modelID, "cohere.embed-v4"):
		return embeddingFamilyCohereV4, nil
	case strings.Contains(modelID, "cohere.embed-english-v3"), strings.Contains(modelID, "cohere.embed-multilingual-v3"):
		return embeddingFamilyCohereV3, nil
	default:
		return 0, fmt.Errorf("bedrock: unsupported embedding model %q", modelID)
	}
}

type embeddingBatch struct {
	vectors        [][]float64
	inputTokens    int64
	hasUsage       bool
	responseBodies []json.RawMessage
}

type titanEmbeddingRequest struct {
	InputText  string `json:"inputText"`
	Dimensions *int64 `json:"dimensions,omitempty"`
	Normalize  *bool  `json:"normalize,omitempty"`
}

type titanEmbeddingResponse struct {
	Embedding           []float64 `json:"embedding"`
	InputTextTokenCount int64     `json:"inputTextTokenCount"`
}

func (e *EmbeddingModel) embedTitan(
	ctx context.Context,
	family embeddingFamily,
	modelID string,
	texts []string,
	dimensions *int64,
	native *EmbeddingRequestOptions,
) (*embeddingBatch, error) {
	if native.InputType != "" {
		return nil, fmt.Errorf("bedrock: embedding extension %q input_type is unsupported by Amazon Titan", EmbeddingRequestExtensionKey)
	}
	if native.Truncate != "" {
		return nil, fmt.Errorf("bedrock: embedding extension %q truncate is unsupported by Amazon Titan", EmbeddingRequestExtensionKey)
	}
	if family == embeddingFamilyTitanV1 {
		if dimensions != nil {
			return nil, errors.New("bedrock: embedding: dimensions are unsupported by Amazon Titan Text Embeddings V1")
		}
		if native.Normalize != nil {
			return nil, fmt.Errorf("bedrock: embedding extension %q normalize is unsupported by Amazon Titan Text Embeddings V1", EmbeddingRequestExtensionKey)
		}
	}
	if family == embeddingFamilyTitanV2 {
		if err := validateDimensions(
			"Amazon Titan Text Embeddings V2",
			dimensions,
			embeddingDimension256,
			embeddingDimension512,
			embeddingDimension1024,
		); err != nil {
			return nil, err
		}
	}

	batch := &embeddingBatch{
		vectors:        make([][]float64, 0, len(texts)),
		hasUsage:       true,
		responseBodies: make([]json.RawMessage, 0, len(texts)),
	}
	for index, text := range texts {
		request := titanEmbeddingRequest{InputText: text}
		if family == embeddingFamilyTitanV2 {
			request.Dimensions = dimensions
			request.Normalize = native.Normalize
		}
		body, err := json.Marshal(request)
		if err != nil {
			return nil, fmt.Errorf("bedrock: encode Titan embedding request %d: %w", index, err)
		}
		responseBody, err := e.invokeEmbedding(ctx, modelID, body)
		if err != nil {
			return nil, fmt.Errorf("bedrock: invoke Titan embedding request %d: %w", index, err)
		}
		var response titanEmbeddingResponse
		if err := json.Unmarshal(responseBody, &response); err != nil {
			return nil, fmt.Errorf("bedrock: decode Titan embedding response %d: %w", index, err)
		}
		if len(response.Embedding) == 0 {
			return nil, fmt.Errorf("bedrock: Titan embedding response %d has no float embedding", index)
		}
		if response.InputTextTokenCount < 0 {
			return nil, fmt.Errorf("bedrock: Titan embedding response %d has negative inputTextTokenCount", index)
		}
		batch.vectors = append(batch.vectors, response.Embedding)
		batch.inputTokens += response.InputTextTokenCount
		batch.responseBodies = append(batch.responseBodies, bytes.Clone(responseBody))
	}
	return batch, nil
}

type cohereEmbeddingRequest struct {
	Texts           []string `json:"texts"`
	InputType       string   `json:"input_type"`
	Truncate        string   `json:"truncate,omitempty"`
	OutputDimension *int64   `json:"output_dimension,omitempty"`
}

type cohereEmbeddingResponse struct {
	Embeddings json.RawMessage `json:"embeddings"`
}

func (e *EmbeddingModel) embedCohere(
	ctx context.Context,
	family embeddingFamily,
	modelID string,
	texts []string,
	dimensions *int64,
	native *EmbeddingRequestOptions,
) (*embeddingBatch, error) {
	if len(texts) > maximumCohereBatchTexts {
		return nil, fmt.Errorf("bedrock: Cohere Embed accepts at most %d texts per request; got %d", maximumCohereBatchTexts, len(texts))
	}
	if err := validateCohereInputType(native.InputType); err != nil {
		return nil, err
	}
	if err := validateCohereTruncate(family, native.Truncate); err != nil {
		return nil, err
	}
	if native.Normalize != nil {
		return nil, fmt.Errorf("bedrock: embedding extension %q normalize is unsupported by Cohere Embed", EmbeddingRequestExtensionKey)
	}
	if family == embeddingFamilyCohereV3 && dimensions != nil {
		return nil, errors.New("bedrock: embedding: dimensions are unsupported by Cohere Embed V3")
	}
	if family == embeddingFamilyCohereV4 {
		if err := validateDimensions(
			"Cohere Embed V4",
			dimensions,
			embeddingDimension256,
			embeddingDimension512,
			embeddingDimension1024,
			embeddingDimension1536,
		); err != nil {
			return nil, err
		}
	}

	request := cohereEmbeddingRequest{
		Texts:     texts,
		InputType: native.InputType,
		Truncate:  native.Truncate,
	}
	if family == embeddingFamilyCohereV4 {
		request.OutputDimension = dimensions
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("bedrock: encode Cohere embedding request: %w", err)
	}
	responseBody, err := e.invokeEmbedding(ctx, modelID, body)
	if err != nil {
		return nil, fmt.Errorf("bedrock: invoke Cohere embedding request: %w", err)
	}

	var response cohereEmbeddingResponse
	if unmarshalErr := json.Unmarshal(responseBody, &response); unmarshalErr != nil {
		return nil, fmt.Errorf("bedrock: decode Cohere embedding response: %w", unmarshalErr)
	}
	vectors, err := decodeCohereFloatEmbeddings(response.Embeddings)
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("bedrock: Cohere embedding response returned %d outputs for %d inputs", len(vectors), len(texts))
	}
	return &embeddingBatch{
		vectors:        vectors,
		responseBodies: []json.RawMessage{bytes.Clone(responseBody)},
	}, nil
}

func validateCohereInputType(inputType string) error {
	switch inputType {
	case "search_document", "search_query", "classification", "clustering":
		return nil
	case "":
		return fmt.Errorf("bedrock: embedding extension %q input_type is required for Cohere Embed", EmbeddingRequestExtensionKey)
	default:
		return fmt.Errorf("bedrock: embedding extension %q has invalid Cohere input_type %q", EmbeddingRequestExtensionKey, inputType)
	}
}

func validateCohereTruncate(family embeddingFamily, truncate string) error {
	if truncate == "" {
		return nil
	}
	if family == embeddingFamilyCohereV3 {
		switch truncate {
		case "NONE", "START", "END":
			return nil
		}
		return fmt.Errorf("bedrock: embedding extension %q has invalid Cohere V3 truncate %q", EmbeddingRequestExtensionKey, truncate)
	}
	switch truncate {
	case "NONE", "LEFT", "RIGHT":
		return nil
	default:
		return fmt.Errorf("bedrock: embedding extension %q has invalid Cohere V4 truncate %q", EmbeddingRequestExtensionKey, truncate)
	}
}

func validateDimensions(model string, dimensions *int64, allowed ...int64) error {
	if dimensions == nil {
		return nil
	}
	if slices.Contains(allowed, *dimensions) {
		return nil
	}
	return fmt.Errorf("bedrock: embedding: dimensions %d are unsupported by %s; allowed values are %v", *dimensions, model, allowed)
}

func decodeCohereFloatEmbeddings(raw json.RawMessage) ([][]float64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("bedrock: Cohere embedding response has no embeddings")
	}
	var vectors [][]float64
	if err := json.Unmarshal(raw, &vectors); err == nil {
		if len(vectors) == 0 {
			return nil, errors.New("bedrock: Cohere embedding response has no float embeddings")
		}
		return vectors, nil
	}
	var byType struct {
		Float [][]float64 `json:"float"`
	}
	if err := json.Unmarshal(raw, &byType); err != nil {
		return nil, fmt.Errorf("bedrock: decode Cohere float embeddings: %w", err)
	}
	if len(byType.Float) == 0 {
		return nil, errors.New("bedrock: Cohere embedding response has no float embeddings")
	}
	return byType.Float, nil
}

func (e *EmbeddingModel) invokeEmbedding(ctx context.Context, modelID string, body []byte) ([]byte, error) {
	output, err := e.api.invokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(modelID),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        body,
	})
	if err != nil {
		return nil, err
	}
	if len(output.Body) == 0 {
		return nil, errors.New("bedrock: InvokeModel returned an empty response body")
	}
	if !json.Valid(output.Body) {
		return nil, errors.New("bedrock: InvokeModel returned invalid JSON")
	}
	return output.Body, nil
}
