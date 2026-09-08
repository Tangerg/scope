package replicate_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/replicate"
)

// Replicate's prediction lifecycle documents six statuses, and only starting
// and processing are still running. A terminal status the poll loop does not
// recognize used to fall through to "keep waiting", so the call spent the whole
// poll timeout on a prediction the provider had already finished with and then
// reported context.DeadlineExceeded — a permanent verdict arriving as local
// slowness, whose obvious remedies (retry, raise the timeout) are both wrong.
//
// aborted is the case that was actually missing: "the prediction exceeded its
// deadline before it could start running".
func TestPollReportsTerminalStatusInsteadOfWaiting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
		want   string
	}{
		{name: "aborted", status: "aborted", want: "aborted"},
		{name: "failed", status: "failed", want: "failed"},
		{name: "canceled", status: "canceled", want: "canceled"},
		{name: "unrecognized", status: "vaporized", want: "unrecognized status"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := modeltest.MuxServer(
				modeltest.Route{Method: "POST", Contains: "/predictions", Handle: func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"pred-1","status":"starting","urls":{"get":"/v1/predictions/pred-1"}}`))
				}},
				modeltest.Route{Method: "GET", Contains: "/predictions/", Handle: func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"pred-1","status":"` + test.status + `"}`))
				}},
			)
			t.Cleanup(server.Close)

			model := newAbortTestModel(t, server)
			request, err := image.NewRequest("a serene mountain lake")
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Call(t.Context(), request)
			if err == nil {
				t.Fatalf("Call() = nil error, want the provider's %q status reported", test.status)
			}
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Call() = %v, want the provider status rather than a local timeout", err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Call() = %v, want an error naming %q", err, test.want)
			}
		})
	}
}

func newAbortTestModel(t *testing.T, server *httptest.Server) *replicate.ImageModel {
	t.Helper()

	options := image.Options{Model: replicate.ModelFluxSchnell}
	if err := options.Validate(); err != nil {
		t.Fatal(err)
	}
	model, err := replicate.NewImageModel(replicate.ImageModelConfig{
		APIKey:         "test-key",
		DefaultOptions: options,
		InputSchema:    replicate.FluxSchnellImageInputSchema(),
		BaseURL:        server.URL,
		PollInterval:   10 * time.Millisecond,
		// Short enough that a regression to "keep waiting" fails as a timeout
		// instead of hanging the suite.
		PollTimeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return model
}
