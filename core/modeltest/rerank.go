package modeltest

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/rerank"
)

const integrationRerankTimeout = 30 * time.Second

type RerankContract struct {
	ModelID string
	// Response must be valid for a three-document query capped at two results.
	Response string

	ExpectedPath string

	Build func(t *testing.T, baseURL string) rerank.Model
}

// RunRerankContract accepts top_n or top_k on the wire.
func RunRerankContract(t *testing.T, contract RerankContract) {
	t.Helper()
	t.Run("Call_Mock", func(t *testing.T) {
		type wireRequest struct {
			Model     string   `json:"model"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
			TopN      *int     `json:"top_n"`
			TopK      *int     `json:"top_k"`
		}
		type observation struct {
			path    string
			request wireRequest
			err     error
		}
		seen := make(chan observation, 1)
		server := JSONServer(http.StatusOK, contract.Response, func(request *http.Request) {
			observed := observation{path: request.URL.Path}
			if err := jsonv2.UnmarshalRead(request.Body, &observed.request); err != nil {
				observed.err = fmt.Errorf("decode request: %w", err)
			}
			select {
			case seen <- observed:
			default:
			}
		})
		t.Cleanup(server.Close)

		model := contract.Build(t, server.URL)
		request, err := rerank.NewRequest("capital of France", []string{"Paris", "Berlin", "Rome"})
		if err != nil {
			t.Fatal(err)
		}
		request.Options.TopK = new(2)
		response, err := model.Call(t.Context(), request)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		select {
		case observation := <-seen:
			if observation.err != nil {
				t.Fatal(observation.err)
			}
			if observation.path != contract.ExpectedPath {
				t.Errorf("URL = %q, want %q", observation.path, contract.ExpectedPath)
			}
			wireRequest := observation.request
			if wireRequest.Model != contract.ModelID || wireRequest.Query != request.Query || len(wireRequest.Documents) != len(request.Documents) {
				t.Fatalf("wire request = %#v", wireRequest)
			}
			limit := wireRequest.TopN
			if limit == nil {
				limit = wireRequest.TopK
			}
			if limit == nil || *limit != *request.Options.TopK {
				t.Fatalf("wire top K = %v, want %d", limit, *request.Options.TopK)
			}
		default:
			t.Fatal("provider sent no decodable request")
		}
		if err := response.ValidateFor(request); err != nil {
			t.Fatalf("response: %v", err)
		}
	})
}

type IntegrationRerankProbe struct {
	Provider string
	Build    func(t *testing.T, key string) rerank.Model
}

// Live results may change ordering; validation checks request-relative bounds.
func RunIntegrationRerank(t *testing.T, probe IntegrationRerankProbe) {
	t.Helper()
	key := RequireKey(t, probe.Provider)
	model := probe.Build(t, key)
	ctx, cancel := WithTimeout(t, integrationRerankTimeout)
	defer cancel()
	request, err := rerank.NewRequest("capital of France", []string{"Paris is the capital of France.", "Berlin is in Germany."})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(ctx, request)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if err := response.ValidateFor(request); err != nil {
		t.Fatalf("response: %v", err)
	}
}
