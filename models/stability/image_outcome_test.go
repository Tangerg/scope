package stability_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/stability"
)

func TestImageTransportCannotProveGenerationOutcome(t *testing.T) {
	for _, test := range []struct {
		name   string
		cause  error
		cancel bool
	}{
		{name: "network", cause: io.ErrUnexpectedEOF},
		{name: "canceled", cause: context.Canceled, cancel: true},
		{name: "nested operation did not execute", cause: &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: io.ErrUnexpectedEOF}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			client := &http.Client{Transport: imageFailureTransport(func(*http.Request) (*http.Response, error) {
				calls++
				if test.cancel {
					cancel()
				}
				return nil, test.cause
			})}
			model, err := stability.NewImageModel(t.Context(), stability.ImageModelConfig{APIKey: "test", BaseURL: "https://stability.test", HTTPClient: client, DefaultOptions: image.Options{Model: stability.ModelCore}})
			if err != nil {
				t.Fatal(err)
			}
			request, err := image.NewRequest("image")
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(ctx, request)
			if response != nil || invocation.ClassifyFailure(err) != invocation.FailureUnknown || !errors.Is(err, test.cause) || calls != 1 {
				t.Fatalf("Call = %+v, %v; requests = %d", response, err, calls)
			}
		})
	}
}

type imageFailureTransport func(*http.Request) (*http.Response, error)

func (i imageFailureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close()
	}
	if request.URL.Host != "stability.test" {
		return nil, errors.New("unexpected external request")
	}
	return i(request)
}
