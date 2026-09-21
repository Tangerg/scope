package modeltest

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/embedding"
)

const integrationEmbeddingTimeout = 30 * time.Second

// EmbeddingContract drives the mock-test contract for any embedding
// vendor. The `Response` field is the canned JSON body the mock server
// returns — it should encode a response with 2 embeddings (matching
// the 2-input request the contract sends).
type EmbeddingContract struct {
	// ModelID is the model id passed into the embedding request.
	ModelID string
	// Response is the canned JSON body — must encode 2 outputs so the
	// contract can validate batching.
	Response string
	// InputField names the provider JSON field containing the ordered input texts.
	InputField string
	// ExpectedEmbeddings is the exact ordered result encoded by Response.
	ExpectedEmbeddings [][]float64
	// ExpectedPath is the URL path the SDK should hit (e.g. "/embeddings"
	// or "/embedding/text"). Empty means skip the path assertion.
	ExpectedPath string
	// Build returns the model wired against the mock server.
	Build func(t *testing.T, baseURL string) embedding.Model
}

// RunEmbeddingContract checks the request and its exact ordered response through
// the provider transport boundary.
func RunEmbeddingContract(t *testing.T, contract EmbeddingContract) {
	t.Helper()
	t.Run("Call_Mock", func(t *testing.T) {
		if contract.ModelID == "" || contract.InputField == "" || len(contract.ExpectedEmbeddings) != 2 {
			t.Fatal("contract requires a model, input field, and two expected embeddings")
		}
		type observation struct {
			path, method string
			body         map[string]json.RawMessage
			err          error
		}
		seen := make(chan observation, 1)
		server := JSONServer(http.StatusOK, contract.Response, func(request *http.Request) {
			observed := observation{path: request.URL.Path, method: request.Method}
			observed.err = json.NewDecoder(request.Body).Decode(&observed.body)
			select {
			case seen <- observed:
			default:
			}
		})
		t.Cleanup(server.Close)
		model := contract.Build(t, server.URL)
		request, err := embedding.NewRequest([]string{"foo", "bar"})
		if err != nil {
			t.Fatal(err)
		}
		request.Options.Model = contract.ModelID
		response, err := model.Call(t.Context(), request)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		select {
		case observed := <-seen:
			if observed.err != nil {
				t.Fatalf("decode request: %v", observed.err)
			}
			if observed.method != http.MethodPost {
				t.Errorf("method = %q; want POST", observed.method)
			}
			if contract.ExpectedPath != "" && observed.path != contract.ExpectedPath {
				t.Errorf("URL = %q; want %q", observed.path, contract.ExpectedPath)
			}
			var modelID string
			if err := json.Unmarshal(observed.body["model"], &modelID); err != nil || modelID != contract.ModelID {
				t.Errorf("wire model = %q, error = %v; want %q", modelID, err, contract.ModelID)
			}
			var texts []string
			if err := json.Unmarshal(observed.body[contract.InputField], &texts); err != nil || !slices.Equal(texts, request.Texts) {
				t.Errorf("wire texts = %q, error = %v; want %q", texts, err, request.Texts)
			}
		default:
			t.Fatal("provider sent no request")
		}
		if err := response.ValidateFor(request); err != nil {
			t.Fatalf("response: %v", err)
		}
		for index, output := range response.Outputs {
			if !slices.Equal(output.Embedding, contract.ExpectedEmbeddings[index]) {
				t.Errorf("output %d = %v; want %v", index, output.Embedding, contract.ExpectedEmbeddings[index])
			}
		}
	})
}

// IntegrationEmbeddingProbe is the standard real-API embedding smoke
// probe: Call returns 2 outputs with non-empty embeddings.
type IntegrationEmbeddingProbe struct {
	Provider string
	Build    func(t *testing.T, key string) embedding.Model
}

// RunIntegrationEmbedding repeats the mock assertions against the live
// service, because a canned body cannot reject an adapter whose auth header,
// path, or request encoding the real vendor would refuse.
func RunIntegrationEmbedding(t *testing.T, probe IntegrationEmbeddingProbe) {
	t.Helper()
	key := RequireKey(t, probe.Provider)
	model := probe.Build(t, key)
	ctx, cancel := WithTimeout(t, integrationEmbeddingTimeout)
	defer cancel()

	request, err := embedding.NewRequest([]string{"the quick brown fox", "jumps over the lazy dog"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(ctx, request)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(response.Outputs) != len(request.Texts) {
		t.Fatalf("got %d outputs; want %d", len(response.Outputs), len(request.Texts))
	}
	for index, output := range response.Outputs {
		if len(output.Embedding) == 0 {
			t.Errorf("output %d has empty embedding", index)
		}
	}
}
