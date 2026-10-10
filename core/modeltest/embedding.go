package modeltest

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/embedding"
)

const integrationEmbeddingTimeout = 30 * time.Second

type EmbeddingContract struct {
	ModelID string
	// Response must encode two embeddings matching ExpectedEmbeddings in order.
	Response string
	// InputField names the provider JSON field containing the ordered input texts.
	InputField string

	ExpectedEmbeddings [][]float64
	// An empty path skips the URL-path assertion.
	ExpectedPath string

	Build func(t *testing.T, baseURL string) embedding.Model
}

func RunEmbeddingContract(t *testing.T, contract EmbeddingContract) {
	t.Helper()
	t.Run("Call_Mock", contract.runCall)
}

func (e EmbeddingContract) runCall(t *testing.T) {
	if e.ModelID == "" || e.InputField == "" || len(e.ExpectedEmbeddings) != 2 {
		t.Fatal("contract requires a model, input field, and two expected embeddings")
	}
	seen := make(chan embeddingObservation, 1)
	server := JSONServer(http.StatusOK, e.Response, func(request *http.Request) {
		observed := embeddingObservation{path: request.URL.Path, method: request.Method}
		observed.err = jsonv2.UnmarshalRead(request.Body, &observed.body)
		select {
		case seen <- observed:
		default:
		}
	})
	t.Cleanup(server.Close)
	model := e.Build(t, server.URL)
	request, err := embedding.NewRequest([]string{"foo", "bar"})
	if err != nil {
		t.Fatal(err)
	}
	request.Options.Model = e.ModelID
	response, err := model.Call(t.Context(), request)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	select {
	case observed := <-seen:
		observed.assert(t, e, request)
	default:
		t.Fatal("provider sent no request")
	}
	if err := response.ValidateFor(request); err != nil {
		t.Fatalf("response: %v", err)
	}
	for index, output := range response.Outputs {
		if !slices.Equal(output.Embedding, e.ExpectedEmbeddings[index]) {
			t.Errorf("output %d = %v; want %v", index, output.Embedding, e.ExpectedEmbeddings[index])
		}
	}
}

type embeddingObservation struct {
	path, method string
	body         map[string]json.RawMessage
	err          error
}

func (e embeddingObservation) assert(t *testing.T, contract EmbeddingContract, request *embedding.Request) {
	t.Helper()
	if e.err != nil {
		t.Fatalf("decode request: %v", e.err)
	}
	if e.method != http.MethodPost {
		t.Errorf("method = %q; want POST", e.method)
	}
	if contract.ExpectedPath != "" && e.path != contract.ExpectedPath {
		t.Errorf("URL = %q; want %q", e.path, contract.ExpectedPath)
	}
	var modelID string
	if err := jsonv2.Unmarshal(e.body["model"], &modelID); err != nil || modelID != contract.ModelID {
		t.Errorf("wire model = %q, error = %v; want %q", modelID, err, contract.ModelID)
	}
	var texts []string
	if err := jsonv2.Unmarshal(e.body[contract.InputField], &texts); err != nil || !slices.Equal(texts, request.Texts) {
		t.Errorf("wire texts = %q, error = %v; want %q", texts, err, request.Texts)
	}
}

type IntegrationEmbeddingProbe struct {
	Provider string
	Build    func(t *testing.T, key string) embedding.Model
}

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
	// ValidateFor owns the complete output/request correspondence: per-output
	// count, consistent dimensions, finite components, and Usage. A weaker local
	// rule here would pass responses the formal contract rejects.
	if err := response.ValidateFor(request); err != nil {
		t.Fatalf("response: %v", err)
	}
}
