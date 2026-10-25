package cohere

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"

	cohereapi "github.com/cohere-ai/cohere-go/v2"

	"github.com/Tangerg/scope/core/embedding"
)

type responseBodyProbe struct {
	*bytes.Reader
	closed int
}

func (r *responseBodyProbe) Close() error { r.closed++; return nil }

type responseTransportProbe struct {
	body  *responseBodyProbe
	calls int
}

func (r *responseTransportProbe) RoundTrip(request *http.Request) (*http.Response, error) {
	r.calls++
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: r.body, Request: request}, nil
}
func TestNativeValidationKeepsSDKBodyOwnership(t *testing.T) {
	body := &responseBodyProbe{Reader: bytes.NewReader([]byte(`{"embeddings":{"float":[[null,1]]}}`))}
	transport := &responseTransportProbe{body: body}
	host := &http.Client{Transport: transport}
	api, err := newAPI(apiConfig{APIKey: "test", BaseURL: "https://provider.test", HTTPClient: host})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.embed(t.Context(), &cohereapi.V2EmbedRequest{Model: "embed-v4.0", Texts: []string{"text"}, InputType: cohereapi.EmbedInputTypeSearchDocument, EmbeddingTypes: []cohereapi.EmbeddingType{cohereapi.EmbeddingTypeFloat}})
	if !errors.Is(err, embedding.ErrInvalidResponse) || transport.calls != 1 || body.closed != 1 || host.Transport != transport {
		t.Fatalf("error=%v requests=%d closes=%d", err, transport.calls, body.closed)
	}
}
func TestNativeBodyCloseDelegatesBeforeRead(t *testing.T) {
	body := &responseBodyProbe{Reader: bytes.NewReader(nil)}
	wrapped := &nativeResponseBody{source: body, embedding: true}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	if body.closed != 1 {
		t.Fatal(body.closed)
	}
	var _ io.ReadCloser = wrapped
}
