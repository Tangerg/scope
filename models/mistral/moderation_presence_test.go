package mistral_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/moderation"
	"github.com/Tangerg/scope/models/mistral"
)

func TestModerationRequiresPairedNativeVerdicts(t *testing.T) {
	for _, result := range []string{`{}`, `{"category_scores":{"sexual":0.9}}`, `{"categories":{"sexual":false}}`, `{"categories":{"sexual":null},"category_scores":{"sexual":0}}`, `{"categories":{"sexual":false},"category_scores":{"sexual":null}}`, `{"categories":{"sexual":false},"category_scores":{"violence":0}}`, `{"categories":{"future":false},"category_scores":{"future":0}}`} {
		t.Run(result, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, `{"results":[`+result+`]}`)
			defer server.Close()
			model, err := mistral.NewModerationModel(t.Context(), mistral.ModerationModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: moderation.Options{Model: "mistral-moderation-latest"}})
			if err != nil {
				t.Fatal(err)
			}
			request, err := moderation.NewRequest([]string{"text"})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if result != `{"categories":{"future":false},"category_scores":{"future":0}}` {
				if !errors.Is(err, moderation.ErrInvalidResponse) {
					t.Fatalf("response=%v error=%v", response, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			categories := response.Outputs[0].Categories
			if len(categories) != 1 || categories["future"].Flagged || categories["future"].Score != 0 {
				t.Fatalf("categories=%v", categories)
			}
		})
	}
}
