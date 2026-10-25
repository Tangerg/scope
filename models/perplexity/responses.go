package perplexity

import (
	"cmp"
	"context"
	"iter"
	"net/http"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

type ResponsesConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (r ResponsesConfig) Validate() error { return r.protocol().Validate() }
func (r ResponsesConfig) protocol() openai.ResponsesConfig {
	return openai.ResponsesConfig{APIKey: r.APIKey, DefaultOptions: r.DefaultOptions, BaseURL: cmp.Or(r.BaseURL, BaseURL), HTTPClient: r.HTTPClient}
}

// Responses implements Perplexity's Agent API through its officially supported
// OpenAI Responses endpoint. It exposes model discovery but no unsupported
// input-token-count capability. Core owns the explicit message history.
type Responses struct{ protocol *openai.Responses }

var (
	_ corechat.Model    = (*Responses)(nil)
	_ corechat.Streamer = (*Responses)(nil)
)

func NewResponses(ctx context.Context, config ResponsesConfig) (*Responses, error) {
	protocol, err := openai.NewCompatibleResponses(ctx, config.protocol(), openai.ResponsesDialect{Provider: "perplexity", PrepareRequest: prepareRequest, DisableRawRequestExtension: true, PreserveStreamEvents: true})
	if err != nil {
		return nil, err
	}
	return &Responses{protocol: protocol}, nil
}
func (r *Responses) Call(ctx context.Context, request *corechat.Request) (*corechat.Response, error) {
	return r.protocol.Call(ctx, request)
}
func (r *Responses) Stream(ctx context.Context, request *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
	return r.protocol.Stream(ctx, request)
}
func (r *Responses) ListModels(ctx context.Context, maxModels int, maxResponseBytes int64) ([]string, error) {
	return r.protocol.ListModels(ctx, maxModels, maxResponseBytes)
}
