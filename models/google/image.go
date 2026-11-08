package google

import (
	"context"
	"errors"
	"net/http"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

const (
	ImageRequestExtensionKey  = Provider + protocol.ImageRequestExtensionSuffix
	ImageResponseExtensionKey = Provider + protocol.ImageResponseExtensionSuffix
)

type ImageModelConfig struct {
	APIKey         string
	BaseURL        string
	HTTPClient     *http.Client
	DefaultOptions image.Options
}

func (i ImageModelConfig) Validate() error {
	if i.APIKey == "" {
		return errors.New("google: APIKey is required")
	}
	if i.DefaultOptions.Model == "" {
		return errors.New("google: DefaultOptions.Model is required")
	}
	if err := i.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

type ImageModel struct {
	transport      *imageTransport
	defaultOptions image.Options
}

var _ image.Model = (*ImageModel)(nil)

func NewImageModel(ctx context.Context, config ImageModelConfig) (*ImageModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	transport, err := newImageTransport(ctx, config)
	if err != nil {
		return nil, err
	}
	return &ImageModel{transport: transport, defaultOptions: config.DefaultOptions.Clone()}, nil
}

func (i *ImageModel) Call(ctx context.Context, req *image.Request) (*image.Response, error) {
	if i == nil || i.transport == nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: errors.New("google: nil ImageModel")}
	}
	if err := req.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	request, err := newImageInteractionRequest(req, i.defaultOptions)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	response, err := i.transport.create(ctx, request)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	return response.response()
}
