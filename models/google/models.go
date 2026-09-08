package google

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
	tts "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/google/internal/protocol"
	openaiprotocol "github.com/Tangerg/scope/models/protocol/openai"
)

// Exported identifiers keep provider-owned names and defaults out of caller literals.
const (
	Provider       = "Google"
	DefaultBaseURL = protocol.DefaultBaseURL
	BaseURLOpenAI  = "https://generativelanguage.googleapis.com/v1beta/openai"

	RequestExtensionKey  = "google/request"
	ResponseExtensionKey = "google/response"

	SpeechRequestExtensionKey         = "google/speech_request"
	SpeechResponseExtensionKey        = "google/speech_response"
	TranscriptionRequestExtensionKey  = "google/transcription_request"
	TranscriptionResponseExtensionKey = "google/transcription_response"
	EmbeddingRequestExtensionKey      = "google/embedding_request"
	EmbeddingResponseExtensionKey     = "google/embedding_response"
	ImageRequestExtensionKey          = "google/image_request"
	ImageResponseExtensionKey         = "google/image_response"

	OpenAIRequestExtensionKey     = "google/openai_request"
	OpenAIResponseExtensionKey    = "google/openai_response"
	OpenAIStreamChunkExtensionKey = "google/openai_stream_chunk"

	ModelGemini36Flash      = protocol.ModelGemini36Flash
	ModelGemini35Flash      = protocol.ModelGemini35Flash
	ModelGemini35FlashLite  = protocol.ModelGemini35FlashLite
	ModelGemini31ProPreview = protocol.ModelGemini31ProPreview

	ModelGemini25FlashPreviewTTS = protocol.ModelGemini25FlashPreviewTTS
	ModelGemini25ProPreviewTTS   = protocol.ModelGemini25ProPreviewTTS
	ModelGemini31FlashTTSPreview = protocol.ModelGemini31FlashTTSPreview

	ModelGemini25FlashImage     = protocol.ModelGemini25FlashImage
	ModelGemini3ProImage        = protocol.ModelGemini3ProImage
	ModelGemini31FlashImage     = protocol.ModelGemini31FlashImage
	ModelGemini31FlashLiteImage = protocol.ModelGemini31FlashLiteImage

	ModelGeminiEmbedding2 = protocol.ModelGeminiEmbedding2
)

const protocolProvider = "google"

func protocolClient(apiKey, baseURL string, httpClient *http.Client) protocol.ClientConfig {
	return protocol.ClientConfig{APIKey: apiKey, BaseURL: baseURL, HTTPClient: httpClient}
}

// ChatConfig binds provider access and defaults shared by every chat call.
type ChatConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (c ChatConfig) Validate() error { return c.protocol().Validate() }

func (c ChatConfig) protocol() protocol.ChatConfig {
	return protocol.ChatConfig{
		Provider: protocolProvider, Client: protocolClient(c.APIKey, c.BaseURL, c.HTTPClient), DefaultOptions: c.DefaultOptions,
	}
}

// Chat wraps this provider's protocol implementation so the wire type stays
// unexported. Callers depend on the Core modality contract, which lets the
// protocol change without breaking this module's public surface.
type Chat struct{ protocol *protocol.Chat }

// NewChat rejects an invalid provider binding before the first chat call.
func NewChat(ctx context.Context, config ChatConfig) (*Chat, error) {
	model, err := protocol.NewChat(ctx, config.protocol())
	if err != nil {
		return nil, err
	}
	return &Chat{protocol: model}, nil
}

func (c *Chat) Call(ctx context.Context, req *corechat.Request) (*corechat.Response, error) {
	if c == nil || c.protocol == nil {
		return nil, errors.New("google: nil Chat")
	}
	return c.protocol.Call(ctx, req)
}

func (c *Chat) Stream(ctx context.Context, req *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
	if c == nil || c.protocol == nil {
		return func(yield func(*corechat.ResponseDelta, error) bool) { yield(nil, errors.New("google: nil Chat")) }
	}
	return c.protocol.Stream(ctx, req)
}

// ChatCompletionsConfig binds provider access and defaults shared by every Chat Completions call.
type ChatCompletionsConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (c ChatCompletionsConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("google: APIKey is required")
	}
	if err := c.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("google: DefaultOptions: %w", err)
	}
	return nil
}

// ChatCompletions is Google's OpenAI-compatible protocol model. It is a distinct
// endpoint from the native Gemini protocol exposed by Chat.
type ChatCompletions = openaiprotocol.ChatCompletions

// NewChatCompletions rejects an invalid provider binding before the first Chat Completions call.
func NewChatCompletions(config ChatCompletionsConfig) (*ChatCompletions, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return openaiprotocol.NewCompatibleChatCompletions(
		openaiprotocol.ChatCompletionsConfig{APIKey: config.APIKey, DefaultOptions: config.DefaultOptions, BaseURL: cmp.Or(config.BaseURL, BaseURLOpenAI), HTTPClient: config.HTTPClient},
		openaiprotocol.Dialect{Provider: protocolProvider, TokenLimitField: openaiprotocol.TokenLimitMaxTokens},
	)
}

// EmbeddingModelConfig binds provider access and defaults shared by every embedding call.
type EmbeddingModelConfig struct {
	APIKey         string
	DefaultOptions embedding.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (e EmbeddingModelConfig) Validate() error { return e.protocol().Validate() }

func (e EmbeddingModelConfig) protocol() protocol.EmbeddingModelConfig {
	return protocol.EmbeddingModelConfig{
		Provider: protocolProvider, Client: protocolClient(e.APIKey, e.BaseURL, e.HTTPClient), DefaultOptions: e.DefaultOptions,
	}
}

// EmbeddingModel wraps this provider's protocol implementation so the wire
// type stays unexported. Callers depend on the Core modality contract, which
// lets the protocol change without breaking this module's public surface.
type EmbeddingModel struct {
	protocol *protocol.EmbeddingModel
}

// NewEmbeddingModel rejects an invalid provider binding before the first embedding call.
func NewEmbeddingModel(ctx context.Context, config EmbeddingModelConfig) (*EmbeddingModel, error) {
	model, err := protocol.NewEmbeddingModel(ctx, config.protocol())
	if err != nil {
		return nil, err
	}
	return &EmbeddingModel{protocol: model}, nil
}

func (e *EmbeddingModel) Call(ctx context.Context, req *embedding.Request) (*embedding.Response, error) {
	if e == nil || e.protocol == nil {
		return nil, errors.New("google: nil EmbeddingModel")
	}
	return e.protocol.Call(ctx, req)
}

// AudioTTSModelConfig binds provider access and defaults shared by every speech call.
type AudioTTSModelConfig struct {
	APIKey         string
	DefaultOptions tts.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (a AudioTTSModelConfig) Validate() error { return a.protocol().Validate() }

func (a AudioTTSModelConfig) protocol() protocol.AudioTTSModelConfig {
	return protocol.AudioTTSModelConfig{
		Provider: protocolProvider, Client: protocolClient(a.APIKey, a.BaseURL, a.HTTPClient), DefaultOptions: a.DefaultOptions,
	}
}

// AudioTTSModel wraps this provider's protocol implementation so the wire
// type stays unexported. Callers depend on the Core modality contract, which
// lets the protocol change without breaking this module's public surface.
type AudioTTSModel struct{ protocol *protocol.AudioTTSModel }

// NewAudioTTSModel rejects an invalid provider binding before the first speech call.
func NewAudioTTSModel(ctx context.Context, config AudioTTSModelConfig) (*AudioTTSModel, error) {
	model, err := protocol.NewAudioTTSModel(ctx, config.protocol())
	if err != nil {
		return nil, err
	}
	return &AudioTTSModel{protocol: model}, nil
}

func (a *AudioTTSModel) Call(ctx context.Context, req *tts.Request) (*tts.Response, error) {
	if a == nil || a.protocol == nil {
		return nil, errors.New("google: nil AudioTTSModel")
	}
	return a.protocol.Call(ctx, req)
}

func (a *AudioTTSModel) Stream(ctx context.Context, req *tts.Request) iter.Seq2[*tts.Response, error] {
	if a == nil || a.protocol == nil {
		return func(yield func(*tts.Response, error) bool) { yield(nil, errors.New("google: nil AudioTTSModel")) }
	}
	if err := req.Validate(); err != nil {
		return func(yield func(*tts.Response, error) bool) { yield(nil, err) }
	}
	return a.protocol.Stream(ctx, req)
}

// AudioTranscriptionModelConfig binds provider access and defaults shared by every transcription call.
type AudioTranscriptionModelConfig struct {
	APIKey         string
	DefaultOptions transcription.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (a AudioTranscriptionModelConfig) Validate() error { return a.protocol().Validate() }

func (a AudioTranscriptionModelConfig) protocol() protocol.AudioTranscriptionModelConfig {
	return protocol.AudioTranscriptionModelConfig{
		Provider: protocolProvider, Client: protocolClient(a.APIKey, a.BaseURL, a.HTTPClient), DefaultOptions: a.DefaultOptions,
	}
}

// AudioTranscriptionModel wraps this provider's protocol implementation so
// the wire type stays unexported. Callers depend on the Core modality
// contract, which lets the protocol change without breaking this module's
// public surface.
type AudioTranscriptionModel struct {
	protocol *protocol.AudioTranscriptionModel
}

// NewAudioTranscriptionModel rejects an invalid provider binding before the first transcription call.
func NewAudioTranscriptionModel(ctx context.Context, config AudioTranscriptionModelConfig) (*AudioTranscriptionModel, error) {
	model, err := protocol.NewAudioTranscriptionModel(ctx, config.protocol())
	if err != nil {
		return nil, err
	}
	return &AudioTranscriptionModel{protocol: model}, nil
}

func (a *AudioTranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if a == nil || a.protocol == nil {
		return nil, errors.New("google: nil AudioTranscriptionModel")
	}
	return a.protocol.Call(ctx, req)
}

// ImageModelConfig binds provider access and defaults shared by every image call.
type ImageModelConfig struct {
	APIKey         string
	DefaultOptions image.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (i ImageModelConfig) Validate() error { return i.protocol().Validate() }

func (i ImageModelConfig) protocol() protocol.ImageModelConfig {
	return protocol.ImageModelConfig{Client: protocolClient(i.APIKey, i.BaseURL, i.HTTPClient), DefaultOptions: i.DefaultOptions}
}

// ImageGenerationOptions carries Gemini Interactions controls that have no
// provider-neutral image option.
type ImageGenerationOptions struct {
	AspectRatio           string                    `json:"aspect_ratio,omitempty"`
	ImageSize             string                    `json:"image_size,omitempty"`
	Delivery              string                    `json:"delivery,omitempty"`
	PreviousInteractionID string                    `json:"previous_interaction_id,omitempty"`
	Store                 *bool                     `json:"store,omitempty"`
	ThinkingLevel         string                    `json:"thinking_level,omitempty"`
	ThinkingSummaries     string                    `json:"thinking_summaries,omitempty"`
	ServiceTier           string                    `json:"service_tier,omitempty"`
	Labels                map[string]string         `json:"labels,omitempty"`
	InputImages           []*media.Media            `json:"input_images,omitempty"`
	GoogleSearch          *ImageGoogleSearchOptions `json:"google_search,omitempty"`
	SafetySettings        []ImageSafetySetting      `json:"safety_settings,omitempty"`
}

// ImageGoogleSearchOptions selects the Google Search sources available during
// image generation.
type ImageGoogleSearchOptions struct {
	SearchTypes []string `json:"search_types,omitempty"`
}

// ImageSafetySetting carries one Gemini image-safety threshold.
type ImageSafetySetting struct {
	Type      string `json:"type"`
	Threshold string `json:"threshold"`
	Method    string `json:"method,omitempty"`
}

// ImageModel wraps this provider's protocol implementation so the wire type
// stays unexported. Callers depend on the Core modality contract, which lets
// the protocol change without breaking this module's public surface.
type ImageModel struct{ protocol *protocol.ImageModel }

// NewImageModel rejects an invalid provider binding before the first image call.
func NewImageModel(ctx context.Context, config ImageModelConfig) (*ImageModel, error) {
	model, err := protocol.NewImageModel(ctx, config.protocol())
	if err != nil {
		return nil, err
	}
	return &ImageModel{protocol: model}, nil
}

func (i *ImageModel) Call(ctx context.Context, req *image.Request) (*image.Response, error) {
	if i == nil || i.protocol == nil {
		return nil, errors.New("google: nil ImageModel")
	}
	return i.protocol.Call(ctx, req)
}

// TextEstimatorConfig binds provider access and the model shared by every estimate.
type TextEstimatorConfig struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
}

func (t TextEstimatorConfig) Validate() error { return t.protocol().Validate() }

func (t TextEstimatorConfig) protocol() protocol.TextEstimatorConfig {
	return protocol.TextEstimatorConfig{
		Client: protocolClient(t.APIKey, t.BaseURL, t.HTTPClient), Model: t.Model,
	}
}

// TextEstimator wraps this provider's protocol implementation so the wire
// type stays unexported. Callers depend on the Core modality contract, which
// lets the protocol change without breaking this module's public surface.
type TextEstimator struct{ protocol *protocol.TextEstimator }

// NewTextEstimator rejects an invalid provider/model binding before estimation begins.
func NewTextEstimator(ctx context.Context, config TextEstimatorConfig) (*TextEstimator, error) {
	estimator, err := protocol.NewTextEstimator(ctx, config.protocol())
	if err != nil {
		return nil, err
	}
	return &TextEstimator{protocol: estimator}, nil
}

func (t *TextEstimator) EstimateText(ctx context.Context, value string) (int, error) {
	if t == nil || t.protocol == nil {
		return 0, errors.New("google: nil TextEstimator")
	}
	return t.protocol.EstimateText(ctx, value)
}
