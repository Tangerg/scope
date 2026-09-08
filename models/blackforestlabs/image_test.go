package blackforestlabs_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/blackforestlabs"
)

func TestImageModel_Call_Mock(t *testing.T) {
	var polls modeltest.PollCounter
	var serverURL string

	srv := modeltest.MuxServer(
		// POST /v1/<model> returns the async id
		modeltest.Route{Method: "POST", Contains: "flux", Handle: func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("x-key") != "test-key" {
				t.Errorf("submit x-key = %q", r.Header.Get("x-key"))
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"task-1","polling_url":%q}`, serverURL+"/v1/get_result?id=task-1")
		}},
		// GET /v1/get_result?id=... polls until Ready
		modeltest.Route{Method: "GET", Contains: "/get_result", Handle: func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("x-key") != "test-key" {
				t.Errorf("poll x-key = %q", r.Header.Get("x-key"))
			}
			n := polls.Inc()
			status := "Pending"
			sample := ""
			if n >= 2 {
				status = "Ready"
				sample = serverURL + "/delivery/img.png"
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"task-1","status":"` + status + `","result":{"sample":"` + sample + `","seed":42,"duration":100}}`))
		}},
		modeltest.Route{Method: "GET", Contains: "/delivery/img.png", Handle: func(w http.ResponseWriter, r *http.Request) {
			if value := r.Header.Get("x-key"); value != "" {
				t.Errorf("download leaked x-key = %q", value)
			}
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("PNG"))
		}},
	)
	serverURL = srv.URL
	t.Cleanup(srv.Close)

	opts := image.Options{Model: "flux-pro-1.1"}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := blackforestlabs.NewImageModel(t.Context(), blackforestlabs.ImageModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
		PollInterval:   10 * time.Millisecond,
		PollTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := image.NewRequest("a small red square")
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.First() == nil {
		t.Fatal("nil result")
	}
	if out.First().Media.Source.Kind != "bytes" {
		t.Fatalf("output source = %q, want bytes", out.First().Media.Source.Kind)
	}
}
