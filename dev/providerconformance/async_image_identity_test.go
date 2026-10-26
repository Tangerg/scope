package providerconformance_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/models/blackforestlabs"
	"github.com/Tangerg/scope/models/luma"
	"github.com/Tangerg/scope/models/replicate"
)

func TestAsyncImageBindsPolledIdentityBeforeDownload(t *testing.T) {
	for _, provider := range []struct {
		name      string
		build     func(context.Context, string) (image.Model, error)
		submitted func(string, string) string
		completed func(string, string) string
	}{
		{"blackforestlabs", func(ctx context.Context, base string) (image.Model, error) {
			return blackforestlabs.NewImageModel(ctx, blackforestlabs.ImageModelConfig{APIKey: "test", BaseURL: base, DefaultOptions: image.Options{Model: blackforestlabs.ModelFlux11Pro}})
		}, func(id, base string) string {
			return fmt.Sprintf(`{"id":%q,"polling_url":%q}`, id, base+"/get_result?id=job-1")
		}, func(id, base string) string {
			return fmt.Sprintf(`{"id":%q,"status":"Ready","result":{"sample":%q}}`, id, base+"/output.png")
		}},
		{"luma", func(ctx context.Context, base string) (image.Model, error) {
			return luma.NewImageModel(ctx, luma.ImageModelConfig{APIKey: "test", BaseURL: base, DefaultOptions: image.Options{Model: luma.ModelUni1}})
		}, func(id, _ string) string {
			return fmt.Sprintf(`{"id":%q,"state":"queued"}`, id)
		}, func(id, base string) string {
			return fmt.Sprintf(`{"id":%q,"state":"completed","created_at":"2026-10-26T00:00:00Z","output":[{"type":"image","url":%q}]}`, id, base+"/output.png")
		}},
		{"replicate", func(ctx context.Context, base string) (image.Model, error) {
			return replicate.NewImageModel(ctx, replicate.ImageModelConfig{APIKey: "test", BaseURL: base, DefaultOptions: image.Options{Model: replicate.ModelFluxSchnell}, InputSchema: replicate.FluxSchnellImageInputSchema()})
		}, func(id, _ string) string {
			return fmt.Sprintf(`{"id":%q,"status":"starting"}`, id)
		}, func(id, base string) string {
			return fmt.Sprintf(`{"id":%q,"status":"succeeded","output":[%q]}`, id, base+"/output.png")
		}},
	} {
		for _, test := range []struct {
			name, submittedID, completedID string
		}{
			{"missing_submission_id", "", "job-1"},
			{"missing_poll_id", "job-1", ""},
			{"changed_poll_id", "job-1", "job-2"},
		} {
			t.Run(provider.name+"/"+test.name, func(t *testing.T) {
				var downloads atomic.Int32
				var server *httptest.Server
				server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					writer.Header().Set("Content-Type", "application/json")
					switch {
					case request.Method == http.MethodPost:
						fmt.Fprint(writer, provider.submitted(test.submittedID, server.URL))
					case request.URL.Path == "/output.png":
						downloads.Add(1)
						writer.Header().Set("Content-Type", "image/png")
						fmt.Fprint(writer, "PNG")
					default:
						fmt.Fprint(writer, provider.completed(test.completedID, server.URL))
					}
				}))
				t.Cleanup(server.Close)
				model, err := provider.build(t.Context(), server.URL)
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), &image.Request{Prompt: "a tree"})
				if !errors.Is(err, image.ErrInvalidResponse) || response != nil || downloads.Load() != 0 {
					t.Fatalf("unbound generation: response=%v error=%v downloads=%d", response, err, downloads.Load())
				}
			})
		}
	}
}
