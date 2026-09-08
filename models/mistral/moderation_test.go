package mistral_test

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/moderation"
	"github.com/Tangerg/scope/models/mistral"
)

const mistralModerationJSON = `{
  "id": "mod-test",
  "model": "mistral-moderation-latest",
  "results": [{
    "categories": {"sexual": false, "violence_and_threats": false},
    "category_scores": {"sexual": 0.01, "violence_and_threats": 0.02}
  }]
}`

func TestModerationModel_Call_Mock(t *testing.T) {
	srv := modeltest.JSONServer(http.StatusOK, mistralModerationJSON)
	t.Cleanup(srv.Close)

	opts := moderation.Options{Model: "mistral-moderation-latest"}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := mistral.NewModerationModel(t.Context(), mistral.ModerationModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := moderation.NewRequest([]string{"hello world"})
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(out.Outputs) == 0 {
		t.Fatal("empty outputs")
	}
}
