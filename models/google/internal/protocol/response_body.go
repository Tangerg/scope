package protocol

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Tangerg/scope/core/embedding"
)

// The SDK converts response JSON through scalar DTOs, losing field presence.
// Keep its authentication and request projection while validating the native
// response before that conversion. Read errors occur outside its retry loop.
type nativeResponseTransport struct {
	next      http.RoundTripper
	embedding *nativeEmbeddingResponse
}

func (n *nativeResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := n.next.RoundTrip(request)
	if err != nil || response == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, err
	}
	path := request.URL.Path
	if strings.HasSuffix(path, ":batchEmbedContents") || strings.HasSuffix(path, ":embedContent") || strings.HasSuffix(path, ":predict") || strings.HasSuffix(path, ":countTokens") {
		if response.Body == nil {
			if response.ContentLength > 0 && request.Method != http.MethodHead {
				return response, nil
			}
			response.Body = http.NoBody
		}
		response.Body = &nativeResponseBody{source: response.Body, tokenCount: strings.HasSuffix(path, ":countTokens"), embedding: n.embedding}
	}
	return response, nil
}

type nativeResponseBody struct {
	tokenCount bool
	source     io.ReadCloser
	read       bool
	reader     *bytes.Reader
	err        error
	embedding  *nativeEmbeddingResponse
}

func (n *nativeResponseBody) Read(buffer []byte) (int, error) {
	if !n.read {
		n.read = true
		var raw []byte
		raw, n.err = io.ReadAll(n.source)
		if n.err == nil {
			n.err = n.validate(raw)
		}
		n.reader = bytes.NewReader(raw)
	}
	if n.err != nil {
		return 0, n.err
	}
	return n.reader.Read(buffer)
}

func (n *nativeResponseBody) Close() error { return n.source.Close() }

func (n *nativeResponseBody) validate(raw []byte) error {
	if n.tokenCount {
		var response struct {
			TotalTokens *int32 `json:"totalTokens"`
		}
		if err := jsonv2.Unmarshal(raw, &response); err != nil {
			return fmt.Errorf("google: decode countTokens response: %w", err)
		}
		if response.TotalTokens == nil || *response.TotalTokens < 0 {
			return fmt.Errorf("google: countTokens response requires nonnegative totalTokens")
		}
		return nil
	}

	response := n.embedding
	if response == nil {
		response = &nativeEmbeddingResponse{}
	}
	return response.decode(raw)
}

type nativeEmbeddingResponse struct {
	Raw                    json.RawMessage
	Embeddings             []*nativeEmbeddingResult
	BillableCharacterCount json.RawMessage
}

type nativeEmbeddingResult struct {
	Values     []*float64
	TokenCount json.RawMessage
	Truncated  json.RawMessage
}

func (n *nativeEmbeddingResponse) decode(raw []byte) error {
	type vector struct {
		Values     []*float64                 `json:"values"`
		Statistics map[string]json.RawMessage `json:"statistics"`
	}
	var wire struct {
		Embeddings  []*vector `json:"embeddings"`
		Embedding   *vector   `json:"embedding"`
		Predictions []*struct {
			Embeddings *vector `json:"embeddings"`
		} `json:"predictions"`
		Metadata struct {
			BillableCharacterCount json.RawMessage `json:"billableCharacterCount"`
		} `json:"metadata"`
		UsageMetadata struct {
			PromptTokenCount json.RawMessage `json:"promptTokenCount"`
		} `json:"usageMetadata"`
		Truncated json.RawMessage `json:"truncated"`
	}
	if err := jsonv2.Unmarshal(raw, &wire); err != nil {
		return fmt.Errorf("%w: google: decode embeddings: %w", embedding.ErrInvalidResponse, err)
	}
	results := make([]*nativeEmbeddingResult, 0, len(wire.Embeddings)+len(wire.Predictions)+1)
	for _, item := range wire.Embeddings {
		if item == nil {
			return fmt.Errorf("%w: google: null embedding", embedding.ErrInvalidResponse)
		}
		results = append(results, &nativeEmbeddingResult{Values: item.Values, TokenCount: item.Statistics["tokenCount"], Truncated: item.Statistics["truncated"]})
	}
	for _, prediction := range wire.Predictions {
		if prediction == nil || prediction.Embeddings == nil {
			return fmt.Errorf("%w: google: null embedding prediction", embedding.ErrInvalidResponse)
		}
		item := prediction.Embeddings
		results = append(results, &nativeEmbeddingResult{Values: item.Values, TokenCount: item.Statistics["token_count"], Truncated: item.Statistics["truncated"]})
	}
	if wire.Embedding != nil {
		results = append(results, &nativeEmbeddingResult{Values: wire.Embedding.Values, TokenCount: wire.UsageMetadata.PromptTokenCount, Truncated: wire.Truncated})
	}
	for index, vector := range results {
		for coordinate, value := range vector.Values {
			if value == nil {
				return fmt.Errorf("%w: google: embedding result %d coordinate %d is null", embedding.ErrInvalidResponse, index, coordinate)
			}
		}
	}
	n.Raw, n.Embeddings, n.BillableCharacterCount = raw, results, wire.Metadata.BillableCharacterCount
	return nil
}
