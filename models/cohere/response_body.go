package cohere

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/rerank"
)

// The SDK's scalar response fields erase missing and null values. Validate
// those facts before its decoder, while leaving requests and retries to the SDK.
type apiHTTPClient struct {
	client *http.Client
	usage  *nativeUsage
}

const (
	embedPath  = "/v2/embed"
	rerankPath = "/v2/rerank"
)

func (a *apiHTTPClient) Do(request *http.Request) (*http.Response, error) {
	response, err := a.client.Do(request)
	if err != nil || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return response, err
	}
	if strings.HasSuffix(request.URL.Path, embedPath) || strings.HasSuffix(request.URL.Path, rerankPath) {
		response.Body = &nativeResponseBody{source: response.Body, embedding: strings.HasSuffix(request.URL.Path, embedPath), usage: a.usage}
	}
	return response, nil
}

type nativeResponseBody struct {
	source    io.ReadCloser
	embedding bool
	read      bool
	reader    *bytes.Reader
	err       error
	usage     *nativeUsage
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
	var response struct {
		Embeddings *struct {
			Float [][]*float64 `json:"float"`
		} `json:"embeddings"`
		Results []struct {
			Index *int     `json:"index"`
			Score *float64 `json:"relevance_score"`
		} `json:"results"`
		Meta nativeUsage `json:"meta"`
	}
	failure := rerank.ErrInvalidResponse
	if n.embedding {
		failure = embedding.ErrInvalidResponse
	}
	if err := jsonv2.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("%w: cohere: decode response: %w", failure, err)
	}
	if n.embedding {
		if response.Embeddings == nil || response.Embeddings.Float == nil {
			return fmt.Errorf("%w: cohere: response requires embeddings.float", embedding.ErrInvalidResponse)
		}
		for index, vector := range response.Embeddings.Float {
			for coordinate, value := range vector {
				if value == nil {
					return fmt.Errorf("%w: cohere: embedding result %d coordinate %d is null", embedding.ErrInvalidResponse, index, coordinate)
				}
			}
		}
	} else {
		if response.Results == nil {
			return fmt.Errorf("%w: cohere: response requires results", rerank.ErrInvalidResponse)
		}
		for index, result := range response.Results {
			if result.Index == nil || result.Score == nil {
				return fmt.Errorf("%w: cohere: rerank result %d requires index and relevance_score", rerank.ErrInvalidResponse, index)
			}
		}
	}
	*n.usage = response.Meta
	return nil
}
