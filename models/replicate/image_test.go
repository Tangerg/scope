package replicate_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/replicate"
)

func TestImageModel_Call_Mock(t *testing.T) {
	var polls modeltest.PollCounter

	var srv *httptest.Server
	srv = modeltest.MuxServer(
		// POST /v1/models/<owner>/<name>/predictions OR /v1/predictions
		modeltest.Route{Method: "POST", Contains: "/predictions", Handle: func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test-key" {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
			var body struct {
				Input map[string]any `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			if body.Input["prompt"] != "a serene mountain lake" || body.Input["output_format"] != "jpg" {
				t.Errorf("input = %#v", body.Input)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"pred-1","status":"starting","urls":{"get":"/v1/predictions/pred-1"}}`))
		}},
		// GET /v1/predictions/<id>
		modeltest.Route{Method: "GET", Contains: "/predictions/", Handle: func(w http.ResponseWriter, r *http.Request) {
			n := polls.Inc()
			status := "processing"
			output := "null"
			if n >= 2 {
				status = "succeeded"
				output = fmt.Sprintf(`[%q,%q]`, srv.URL+"/img-1.png", srv.URL+"/img-2.png")
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"pred-1","status":"` + status + `","output":` + output + `}`))
		}},
		modeltest.Route{Method: "GET", Contains: "/img-", Handle: func(w http.ResponseWriter, r *http.Request) {
			if authorization := r.Header.Get("Authorization"); authorization != "Bearer test-key" {
				t.Errorf("output download Authorization = %q", authorization)
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("PNG"))
		}},
	)
	t.Cleanup(srv.Close)

	opts := image.Options{Model: replicate.ModelFluxSchnell}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.OutputFormat = "image/jpeg"
	m, err := replicate.NewImageModel(t.Context(), replicate.ImageModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		InputSchema:    replicate.FluxSchnellImageInputSchema(),
		BaseURL:        srv.URL,
		PollInterval:   10 * time.Millisecond,
		PollTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := image.NewRequest("a serene mountain lake")
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(out.Outputs) != 2 || out.First() == nil {
		t.Fatalf("outputs = %#v", out.Outputs)
	}
}
