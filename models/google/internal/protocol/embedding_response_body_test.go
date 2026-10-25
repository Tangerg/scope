package protocol

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
)

type embeddingBodyProbe struct {
	*bytes.Reader
	closed int
}

func (e *embeddingBodyProbe) Close() error { e.closed++; return nil }
func TestEmbeddingBodyOwnsNoTransportOrClosePolicy(t *testing.T) {
	for _, raw := range []string{`{"embeddings":[{"values":[null,1]}]}`, `{"embeddings":[{"values":[0,1]}]}`} {
		t.Run(raw, func(t *testing.T) {
			source := &embeddingBodyProbe{Reader: bytes.NewReader([]byte(raw))}
			body := &nativeResponseBody{source: source}
			data, err := io.ReadAll(body)
			if bytes.Contains([]byte(raw), []byte("null")) {
				if !errors.Is(err, embedding.ErrInvalidResponse) {
					t.Fatal(err)
				}
			} else if err != nil || string(data) != raw {
				t.Fatalf("body=%s error=%v", data, err)
			}
			if source.closed != 0 {
				t.Fatal("validation closed borrowed response body")
			}
			if err := body.Close(); err != nil {
				t.Fatal(err)
			}
			if source.closed != 1 {
				t.Fatal(source.closed)
			}
		})
	}
}

type nilNativeBodyTransport struct{ contentLength int64 }

func (n nilNativeBodyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, ContentLength: n.contentLength, Header: make(http.Header), Request: request}, nil
}

func TestNativeBodyPreservesHTTPClientNilBodyContract(t *testing.T) {
	for _, contentLength := range []int64{0, 1} {
		client := &http.Client{Transport: &nativeResponseTransport{next: nilNativeBodyTransport{contentLength: contentLength}}}
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.test/model:countTokens", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if contentLength > 0 {
			if err == nil {
				t.Fatal("client accepted a missing body with a positive content length")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(response.Body)
		if err == nil {
			t.Fatal("empty native response fabricated a token count")
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
