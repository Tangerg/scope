package protocol

import (
	"context"
	"errors"
	"iter"
	"net/http"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/invocation"
)

type ClientConfig struct {
	APIKey string

	// Backend selects the genai backend. Zero value falls back to
	// [genai.BackendGeminiAPI] — the public Gemini API. Set to
	// [genai.BackendVertexAI] for GCP-hosted enterprise deployments;
	// Project and Location become required in that mode and APIKey
	// is ignored in favor of the supplied [genai.ClientConfig.Credentials]
	// (or ADC).
	Backend genai.Backend

	// Project is the GCP project id, required when Backend ==
	// BackendVertexAI. Ignored otherwise.
	Project string

	// Location is the GCP region (e.g. "us-central1"), required when
	// Backend == BackendVertexAI. Ignored otherwise.
	Location string

	// BaseURL overrides the genai client endpoint. Optional —
	// production users should leave it empty (the SDK picks the right
	// host per Backend). Useful for mock servers / corporate proxies.
	BaseURL    string
	APIVersion string

	// HTTPClient remains owned by the caller; the SDK borrows a shallow copy.
	HTTPClient *http.Client
}

func (c ClientConfig) Validate() error {
	if c.Backend == genai.BackendVertexAI {
		if c.Project == "" {
			return errors.New("google: Vertex AI project is required")
		}
		if c.Location == "" {
			return errors.New("google: Vertex AI location is required")
		}
		return nil
	}
	if c.APIKey == "" {
		return errors.New("google: APIKey is required")
	}
	return nil
}

type api struct {
	client     *genai.Client
	transport  http.RoundTripper
	generation *GenerationClient
}

// newAPI takes a context because construction reaches the network: on the
// Vertex AI backend with application default credentials, genai.NewClient
// resolves them and then asks the resolved credential for its quota project,
// which is a metadata-server call the context governs. A background context
// there would leave a caller unable to bound or cancel its own wiring.
func newAPI(ctx context.Context, config ClientConfig) (*api, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	clientCfg := &genai.ClientConfig{
		Backend:    config.Backend,
		HTTPClient: config.HTTPClient,
	}
	if config.HTTPClient != nil {
		sdkClient := *config.HTTPClient
		clientCfg.HTTPClient = &sdkClient
	}
	if config.Backend == 0 {
		clientCfg.Backend = genai.BackendGeminiAPI
	}
	if config.APIKey != "" {
		clientCfg.APIKey = config.APIKey
	}
	if config.Project != "" {
		clientCfg.Project = config.Project
	}
	if config.Location != "" {
		clientCfg.Location = config.Location
	}
	if config.BaseURL != "" {
		clientCfg.HTTPOptions.BaseURL = config.BaseURL
	}
	if config.APIVersion != "" {
		clientCfg.HTTPOptions.APIVersion = config.APIVersion
	}

	client, err := genai.NewClient(ctx, clientCfg)
	if err != nil {
		return nil, err
	}
	resolvedConfig := client.ClientConfig()
	sdkClient := resolvedConfig.HTTPClient
	boundClient, err := httpclient.New(ctx, httpclient.Config{HTTPClient: sdkClient, BaseURL: resolvedConfig.HTTPOptions.BaseURL})
	if err != nil {
		return nil, err
	}
	*sdkClient = *boundClient
	transport := sdkClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	sdkClient.Transport = &nativeResponseTransport{next: transport}

	return &api{client: client, transport: transport, generation: &GenerationClient{config: client.ClientConfig()}}, nil
}

func (a *api) chatCompletion(ctx context.Context, modelName string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	return a.wrapResult(a.generation.GenerateContent(ctx, modelName, contents, config))
}

func (a *api) chatCompletionStream(ctx context.Context, modelName string, contents []*genai.Content, config *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
	return a.wrapSequence(a.generation.GenerateContentStream(ctx, modelName, contents, config))
}

func (a *api) embedding(ctx context.Context, request *embeddingRequest) (*nativeEmbeddingResponse, error) {
	// Each call owns its raw response. Clone only SDK wiring; the host's HTTP
	// policy and already-resolved authentication transport remain borrowed.
	response := &nativeEmbeddingResponse{}
	clientConfig := a.client.ClientConfig()
	if err := request.configure(clientConfig.Backend); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	clientConfig.HTTPOptions.Headers = clientConfig.HTTPOptions.Headers.Clone()
	httpClient := *clientConfig.HTTPClient
	httpClient.Transport = &nativeResponseTransport{next: a.transport, embedding: response, embeddingRequest: request}
	clientConfig.HTTPClient = &httpClient
	if clientConfig.Backend == genai.BackendGeminiAPI {
		clientConfig.Project, clientConfig.Location = "", ""
	}
	client, err := genai.NewClient(ctx, &clientConfig)
	if err != nil {
		return nil, err
	}
	if _, err := client.Models.EmbedContent(ctx, request.model, request.contents, request.config); err != nil {
		return nil, a.wrapError(err)
	}
	return response, nil
}

func (a *api) countTokens(ctx context.Context, modelName string, contents []*genai.Content, config *genai.CountTokensConfig) (*genai.CountTokensResponse, error) {
	response, err := a.wrapResult(a.client.Models.CountTokens(ctx, modelName, contents, config))
	if err != nil {
		return nil, err
	}
	if response == nil || response.TotalTokens < 0 {
		return nil, errors.New("google: invalid totalTokens count")
	}
	return response, nil
}

func (*api) wrapError(err error) error {
	if err == nil {
		return nil
	}
	type httpError interface {
		error
		HTTPStatus() int
		HTTPHeader() http.Header
	}
	if _, ok := errors.AsType[httpError](err); ok {
		return err
	}
	apiErr, ok := errors.AsType[genai.APIError](err)
	if !ok {
		return err
	}
	return &responseError{err: err, status: apiErr.Code}
}

func (a *api) wrapResult[T any](value *T, err error) (*T, error) {
	return value, a.wrapError(err)
}

func (a *api) wrapSequence[T any](sequence iter.Seq2[*T, error]) iter.Seq2[*T, error] {
	return func(yield func(*T, error) bool) {
		for value, err := range sequence {
			if !yield(value, a.wrapError(err)) {
				return
			}
		}
	}
}
