package blackforestlabs_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/blackforestlabs"
)

// BFL's get_result reference declares eight statuses, and only Pending,
// Reasoning and Generating are still running -- the last two were added after
// this adapter was written, which is how the loop's old "anything else means
// keep waiting" default happened to stay correct. It no longer relies on luck:
// continuing to poll is safe only for a state known to advance, so an
// unrecognized one is reported rather than left to expire as this call's own
// timeout, whose obvious remedies -- retry, a longer deadline -- are both wrong.
func TestPollReportsAnUnrecognizedStatus(t *testing.T) {
	t.Parallel()

	var serverURL string
	server := modeltest.MuxServer(
		modeltest.Route{Method: "POST", Contains: "flux", Handle: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"task-1","polling_url":%q}`, serverURL+"/v1/get_result?id=task-1")
		}},
		modeltest.Route{Method: "GET", Contains: "/get_result", Handle: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"task-1","status":"Transcending"}`))
		}},
	)
	serverURL = server.URL
	t.Cleanup(server.Close)

	options := image.Options{Model: "flux-pro-1.1"}
	if err := options.Validate(); err != nil {
		t.Fatal(err)
	}
	model, err := blackforestlabs.NewImageModel(t.Context(), blackforestlabs.ImageModelConfig{
		APIKey:         "test-key",
		DefaultOptions: options,
		BaseURL:        server.URL + "/v1",
		PollInterval:   10 * time.Millisecond,
		// Short enough that a regression to "keep waiting" fails as a timeout
		// instead of hanging the suite.
		PollTimeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	request, err := image.NewRequest("a serene mountain lake")
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Call(t.Context(), request)
	if err == nil {
		t.Fatal("Call() = nil error, want the unrecognized status reported")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call() = %v, want the provider status rather than a local timeout", err)
	}
	if !strings.Contains(err.Error(), "Transcending") {
		t.Fatalf("Call() = %v, want an error naming the status", err)
	}
}
