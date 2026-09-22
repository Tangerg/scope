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

// RerankContract drives the mock transport contract for a reranking provider.
type RerankContract struct {
	// ModelID is the model id the adapter is expected to put on the wire, so a
	// silently substituted default is caught rather than accepted.
	ModelID string
	// Response is the canned JSON body. It must decode to a result set that is
	// valid for a three-document query capped at two, because the contract
	// checks the response against the request it actually sent.
	Response string
	// ExpectedPath is the URL path the SDK should reach.
	ExpectedPath string
	// Build returns the model wired against the mock server.
	Build func(t *testing.T, baseURL string) rerank.Model
}

// RunRerankContract accepts the cap under either top_n or top_k because
// reranking vendors disagree on the name. What matters is that the portable
// Options.TopK reaches the wire at all: an adapter that drops it returns a
// plausible ranking over every document and would otherwise look correct.
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
			var decoded wireRequest
			if err := jsonv2.UnmarshalRead(request.Body, &decoded); err != nil {
				seen <- observation{path: request.URL.Path, err: fmt.Errorf("decode request: %w", err)}
				return
			}
			seen <- observation{path: request.URL.Path, request: decoded}
		})
		t.Cleanup(server.Close)

		model := contract.Build(t, server.URL)
		request, err := rerank.NewRequest("capital of France", []string{"Paris", "Berlin", "Rome"})
		if err != nil {
			t.Fatal(err)
		}
		request.Options.TopK = 2
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
			if limit == nil || *limit != request.Options.TopK {
				t.Fatalf("wire top K = %v, want %d", limit, request.Options.TopK)
			}
		default:
			t.Fatal("provider sent no decodable request")
		}
		if err := response.ValidateFor(request); err != nil {
			t.Fatalf("response: %v", err)
		}
	})
}

// IntegrationRerankProbe is the standard real-API rerank smoke probe.
type IntegrationRerankProbe struct {
	Provider string
	Build    func(t *testing.T, key string) rerank.Model
}

// RunIntegrationRerank leans on ValidateFor rather than on an expected
// ordering, because a live model may legitimately rank two documents
// differently between calls while still owing index-addressed, in-range
// results for the query it was given.
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
