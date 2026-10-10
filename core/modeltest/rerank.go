package modeltest

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"slices"
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
	t.Run("Call_Mock", contract.runCall)
}

func (r RerankContract) runCall(t *testing.T) {
	seen := make(chan rerankObservation, 1)
	server := JSONServer(http.StatusOK, r.Response, func(request *http.Request) {
		observed := rerankObservation{path: request.URL.Path}
		if err := jsonv2.UnmarshalRead(request.Body, &observed.request); err != nil {
			observed.err = fmt.Errorf("decode request: %w", err)
		}
		select {
		case seen <- observed:
		default:
		}
	})
	t.Cleanup(server.Close)

	model := r.Build(t, server.URL)
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
	case observed := <-seen:
		observed.assert(t, r, request)
	default:
		t.Fatal("provider sent no decodable request")
	}
	if err := response.ValidateFor(request); err != nil {
		t.Fatalf("response: %v", err)
	}
}

type rerankWireRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      *int     `json:"top_n"`
	TopK      *int     `json:"top_k"`
}

type rerankObservation struct {
	path    string
	request rerankWireRequest
	err     error
}

func (r rerankObservation) assert(t *testing.T, contract RerankContract, request *rerank.Request) {
	t.Helper()
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.path != contract.ExpectedPath {
		t.Errorf("URL = %q, want %q", r.path, contract.ExpectedPath)
	}
	wire := r.request
	// The returned result carries only indices, so the oracle must confirm the
	// adapter sent the exact documents in order; a length check alone would miss
	// reordered or substituted content.
	if wire.Model != contract.ModelID || wire.Query != request.Query || !slices.Equal(wire.Documents, request.Documents) {
		t.Fatalf("wire request = %#v", wire)
	}
	limit := wire.TopN
	if limit == nil {
		limit = wire.TopK
	}
	if limit == nil || *limit != *request.Options.TopK {
		t.Fatalf("wire top K = %v, want %d", limit, *request.Options.TopK)
	}
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
