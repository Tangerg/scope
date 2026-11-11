package modeltest

import (
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
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
	// LimitField is the wire member carrying the effective result limit, and
	// ResultsField the response member listing {index, relevance_score} items.
	LimitField, ResultsField string

	// Build constructs the model against baseURL with defaults as its
	// DefaultOptions.
	Build func(t *testing.T, baseURL string, defaults rerank.Options) rerank.Model
}

// RunRerankContract checks the wire request and the effective result limit:
// Core owns how a request limit, the default and the document count combine,
// and how many results the response must then hold.
func RunRerankContract(t *testing.T, contract RerankContract) {
	t.Helper()
	t.Run("Call_Mock", contract.runCall)
	t.Run("EffectiveLimit", contract.runEffectiveLimit)
}

func (r RerankContract) runCall(t *testing.T) {
	seen := make(chan rerankObservation, 1)
	server := JSONServer(http.StatusOK, r.Response, func(request *http.Request) {
		observed := rerankObservation{path: request.URL.Path}
		body, err := io.ReadAll(request.Body)
		if err == nil {
			err = errors.Join(jsonv2.Unmarshal(body, &observed.request), jsonv2.Unmarshal(body, &observed.members))
		}
		if err != nil {
			observed.err = fmt.Errorf("decode request: %w", err)
		}
		select {
		case seen <- observed:
		default:
		}
	})
	t.Cleanup(server.Close)

	model := r.Build(t, server.URL, rerank.Options{Model: r.ModelID})
	request, err := rerank.NewRequest("capital of France", []string{"Paris", "Berlin", "Rome"})
	if err != nil {
		t.Fatal(err)
	}
	request.Options.TopK = new(2)
	response, err := model.Call(t.Context(), request.Clone())
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

func (r RerankContract) runEffectiveLimit(t *testing.T) {
	for _, test := range []struct {
		name               string
		defaults, override *int
		// wireLimit is the limit the provider must send; nil sends none.
		wireLimit *int
		results   int
		wantError bool
	}{
		{name: "inherit default", defaults: new(1), wireLimit: new(1), results: 1},
		{name: "explicit all", defaults: new(1), override: new(0), results: 2},
		{name: "explicit override", defaults: new(1), override: new(2), wireLimit: new(2), results: 2},
		{name: "default capped at documents", defaults: new(3), wireLimit: new(2), results: 2},
		{name: "default response mismatch", defaults: new(1), wireLimit: new(1), results: 2, wantError: true},
		{name: "all response mismatch", defaults: new(1), override: new(0), results: 1, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			items := []string{`{"index":0,"relevance_score":0.9}`, `{"index":1,"relevance_score":0.4}`}[:test.results]
			observed := make(chan *int, 1)
			server := JSONServer(http.StatusOK, fmt.Sprintf(`{%q:[%s]}`, r.ResultsField, strings.Join(items, ",")), func(request *http.Request) {
				var body map[string]jsontext.Value
				var limit *int
				if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
					t.Error(err)
				} else if raw, present := body[r.LimitField]; present {
					if err := jsonv2.Unmarshal(raw, &limit); err != nil {
						t.Error(err)
					}
				}
				observed <- limit
			})
			t.Cleanup(server.Close)
			model := r.Build(t, server.URL, rerank.Options{Model: r.ModelID, TopK: test.defaults})
			request := &rerank.Request{Query: "query", Documents: []string{"first", "second"}, Options: rerank.Options{TopK: test.override}}
			response, err := model.Call(t.Context(), request)
			if (err != nil) != test.wantError {
				t.Fatalf("response=%v error=%v", response, err)
			}
			if limit := <-observed; (limit == nil) != (test.wireLimit == nil) || limit != nil && *limit != *test.wireLimit {
				t.Fatalf("wire %s = %v, want %v", r.LimitField, limit, test.wireLimit)
			}
			if request.Options.TopK != test.override || request.Options.Model != "" {
				t.Fatal("caller options mutated")
			}
		})
	}
}

type rerankWireRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

// rerankObservation keeps every member as well, since each provider names
// its limit member differently.
type rerankObservation struct {
	path    string
	request rerankWireRequest
	members map[string]jsontext.Value
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
	var limit int
	if err := jsonv2.Unmarshal(r.members[contract.LimitField], &limit); err != nil || limit != *request.Options.TopK {
		t.Fatalf("wire %s = %s, want %d", contract.LimitField, r.members[contract.LimitField], *request.Options.TopK)
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
	ctx, cancel := context.WithTimeout(t.Context(), integrationRerankTimeout)
	defer cancel()
	request, err := rerank.NewRequest("capital of France", []string{"Paris is the capital of France.", "Berlin is in Germany."})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(ctx, request.Clone())
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if err := response.ValidateFor(request); err != nil {
		t.Fatalf("response: %v", err)
	}
}
