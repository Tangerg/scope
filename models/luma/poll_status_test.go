package luma_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/models/luma"
)

// The pinned SDK declares four generation states, and the poll loop waits only
// on the two that are still moving. An unrecognized one is reported rather than
// absorbed: continuing to poll is safe only for a state known to advance, so a
// state added later would otherwise arrive as this call's own timeout, whose
// obvious remedies -- retry, a longer deadline -- are both wrong.
func TestPollReportsAnUnrecognizedState(t *testing.T) {
	t.Parallel()

	server := muxServer(
		route{Method: "POST", Contains: "/generations", Handle: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"gen-1","created_at":"2026-07-31T08:00:00Z","model":"uni-1","state":"queued","type":"image","output":[]}`))
		}},
		route{Method: "GET", Contains: "/generations/", Handle: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"gen-1","created_at":"2026-07-31T08:00:00Z","model":"uni-1","state":"transcending","type":"image","output":[]}`))
		}},
	)
	t.Cleanup(server.Close)

	options := image.Options{Model: luma.ModelUni1}
	if err := options.Validate(); err != nil {
		t.Fatal(err)
	}
	model, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{
		APIKey:         "test-key",
		DefaultOptions: options,
		BaseURL:        server.URL,
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
		t.Fatal("Call() = nil error, want the unrecognized state reported")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call() = %v, want the provider state rather than a local timeout", err)
	}
	if !strings.Contains(err.Error(), "transcending") {
		t.Fatalf("Call() = %v, want an error naming the state", err)
	}
}
