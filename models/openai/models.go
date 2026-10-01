package openai

import (
	"context"
	"net/http"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/moderation"
	tts "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	openaiprotocol "github.com/Tangerg/scope/models/protocol/openai"
)

// Exported identifiers keep provider-owned names and defaults out of caller literals.
const (
	Provider         = "OpenAI"
	protocolProvider = "openai"

	RequestExtensionKey     = "openai/request"
	ResponseExtensionKey    = "openai/response"
	StreamChunkExtensionKey = "openai/stream_chunk"

	ResponsesRequestExtensionKey  = "openai/responses_request"
	ResponsesResponseExtensionKey = "openai/responses_response"

	SpeechRequestExtensionKey        = "openai/speech_request"
	TranscriptionRequestExtensionKey = "openai/transcription_request"
	TranslationRequestExtensionKey   = "openai/translation_request"
	EmbeddingRequestExtensionKey     = "openai/embedding_request"
	ImageRequestExtensionKey         = "openai/image_request"
	ModerationRequestExtensionKey    = "openai/moderation_request"
)

// ChatCompletionsConfig configures the OpenAI Chat Completions endpoint. Construction does
// no network I/O; per-call overrides remain in chat.Request.
type ChatCompletionsConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (c ChatCompletionsConfig) Validate() error { return c.protocol().Validate() }

func (c ChatCompletionsConfig) protocol() openaiprotocol.ChatCompletionsConfig {
	return openaiprotocol.ChatCompletionsConfig{
		APIKey:         c.APIKey,
		DefaultOptions: c.DefaultOptions,
		BaseURL:        c.BaseURL,
		HTTPClient:     c.HTTPClient,
	}
}

// NewChatCompletions rejects an invalid provider binding before the first chat call.
func NewChatCompletions(ctx context.Context, config ChatCompletionsConfig) (*openaiprotocol.ChatCompletions, error) {
	return openaiprotocol.NewChatCompletions(ctx, config.protocol())
}

// ResponsesConfig configures the OpenAI Responses endpoint independently from
// Chat Completions because their transport capabilities differ.
type ResponsesConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (r ResponsesConfig) Validate() error { return r.protocol().Validate() }

func (r ResponsesConfig) protocol() openaiprotocol.ResponsesConfig {
	return openaiprotocol.ResponsesConfig{
		APIKey:         r.APIKey,
		DefaultOptions: r.DefaultOptions,
		BaseURL:        r.BaseURL,
		HTTPClient:     r.HTTPClient,
	}
}

// NewResponses rejects an invalid provider binding before the first Responses call.
func NewResponses(ctx context.Context, config ResponsesConfig) (*openaiprotocol.Responses, error) {
	return openaiprotocol.NewResponses(ctx, config.protocol())
}

// EmbeddingModelConfig binds provider access and defaults shared by every embedding call.
type EmbeddingModelConfig struct {
	APIKey         string
	DefaultOptions embedding.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (e EmbeddingModelConfig) Validate() error { return e.protocol().Validate() }

func (e EmbeddingModelConfig) protocol() openaiprotocol.EmbeddingModelConfig {
	return openaiprotocol.EmbeddingModelConfig{Provider: protocolProvider, APIKey: e.APIKey, DefaultOptions: e.DefaultOptions, BaseURL: e.BaseURL, HTTPClient: e.HTTPClient}
}

// NewEmbeddingModel rejects an invalid provider binding before the first embedding call.
func NewEmbeddingModel(ctx context.Context, config EmbeddingModelConfig) (*openaiprotocol.EmbeddingModel, error) {
	return openaiprotocol.NewEmbeddingModel(ctx, config.protocol())
}

// AudioTranscriptionModelConfig binds provider access and defaults shared by every transcription call.
type AudioTranscriptionModelConfig struct {
	APIKey         string
	DefaultOptions transcription.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (a AudioTranscriptionModelConfig) Validate() error { return a.protocol().Validate() }

func (a AudioTranscriptionModelConfig) protocol() openaiprotocol.AudioTranscriptionModelConfig {
	return openaiprotocol.AudioTranscriptionModelConfig{Provider: protocolProvider, APIKey: a.APIKey, DefaultOptions: a.DefaultOptions, BaseURL: a.BaseURL, HTTPClient: a.HTTPClient}
}

// NewAudioTranscriptionModel rejects an invalid provider binding before the first transcription call.
func NewAudioTranscriptionModel(ctx context.Context, config AudioTranscriptionModelConfig) (*openaiprotocol.AudioTranscriptionModel, error) {
	return openaiprotocol.NewAudioTranscriptionModel(ctx, config.protocol())
}

// AudioTranslationModelConfig binds provider access and defaults shared by every translation call.
type AudioTranslationModelConfig struct {
	APIKey         string
	DefaultOptions transcription.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (a AudioTranslationModelConfig) Validate() error { return a.protocol().Validate() }

func (a AudioTranslationModelConfig) protocol() openaiprotocol.AudioTranslationModelConfig {
	return openaiprotocol.AudioTranslationModelConfig{Provider: protocolProvider, APIKey: a.APIKey, DefaultOptions: a.DefaultOptions, BaseURL: a.BaseURL, HTTPClient: a.HTTPClient}
}

// NewAudioTranslationModel rejects an invalid provider binding before the first translation call.
func NewAudioTranslationModel(ctx context.Context, config AudioTranslationModelConfig) (*openaiprotocol.AudioTranslationModel, error) {
	return openaiprotocol.NewAudioTranslationModel(ctx, config.protocol())
}

// AudioTTSModelConfig binds provider access and defaults shared by every speech call.
type AudioTTSModelConfig struct {
	APIKey           string
	DefaultOptions   tts.Options
	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

func (a AudioTTSModelConfig) Validate() error { return a.protocol().Validate() }

func (a AudioTTSModelConfig) protocol() openaiprotocol.AudioTTSModelConfig {
	return openaiprotocol.AudioTTSModelConfig{
		Provider:         protocolProvider,
		APIKey:           a.APIKey,
		DefaultOptions:   a.DefaultOptions,
		BaseURL:          a.BaseURL,
		HTTPClient:       a.HTTPClient,
		MaxResponseBytes: a.MaxResponseBytes,
	}
}

// NewAudioTTSModel rejects an invalid provider binding before the first speech call.
func NewAudioTTSModel(ctx context.Context, config AudioTTSModelConfig) (*openaiprotocol.AudioTTSModel, error) {
	return openaiprotocol.NewAudioTTSModel(ctx, config.protocol())
}

// ImageModelConfig binds provider access and defaults shared by every image call.
type ImageModelConfig struct {
	APIKey         string
	DefaultOptions image.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (i ImageModelConfig) Validate() error { return i.protocol().Validate() }

func (i ImageModelConfig) protocol() openaiprotocol.ImageModelConfig {
	return openaiprotocol.ImageModelConfig{Provider: protocolProvider, APIKey: i.APIKey, DefaultOptions: i.DefaultOptions, BaseURL: i.BaseURL, HTTPClient: i.HTTPClient}
}

// NewImageModel rejects an invalid provider binding before the first image call.
func NewImageModel(ctx context.Context, config ImageModelConfig) (*openaiprotocol.ImageModel, error) {
	return openaiprotocol.NewImageModel(ctx, config.protocol())
}

// ModerationModelConfig binds provider access and defaults shared by every moderation call.
type ModerationModelConfig struct {
	APIKey         string
	DefaultOptions moderation.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (m ModerationModelConfig) Validate() error { return m.protocol().Validate() }

func (m ModerationModelConfig) protocol() openaiprotocol.ModerationModelConfig {
	return openaiprotocol.ModerationModelConfig{Provider: protocolProvider, APIKey: m.APIKey, DefaultOptions: m.DefaultOptions, BaseURL: m.BaseURL, HTTPClient: m.HTTPClient}
}

// NewModerationModel rejects an invalid provider binding before the first moderation call.
func NewModerationModel(ctx context.Context, config ModerationModelConfig) (*openaiprotocol.ModerationModel, error) {
	return openaiprotocol.NewModerationModel(ctx, config.protocol())
}
