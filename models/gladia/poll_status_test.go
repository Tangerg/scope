package gladia_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/gladia"
)

// Gladia documents four statuses for a pre-recorded job, and the poll loop
// waits only on the two that are still moving. An unrecognized one is reported
// rather than absorbed: continuing to poll is safe only for a state known to
// advance, so a status added later would otherwise arrive as this call's own
// timeout, whose obvious remedies -- retry, a longer deadline -- are both wrong.
func TestPollReportsAnUnrecognizedStatus(t *testing.T) {
	t.Parallel()

	server := modeltest.MuxServer(
		modeltest.Route{Method: "POST", Contains: "/upload", Handle: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"audio_url":"https://cdn.test/audio.bin"}`))
		}},
		modeltest.Route{Method: "POST", Contains: "/pre-recorded", Handle: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"job-1","result_url":"/v2/pre-recorded/job-1"}`))
		}},
		modeltest.Route{Method: "GET", Contains: "/pre-recorded/", Handle: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"job-1","status":"transcending"}`))
		}},
	)
	t.Cleanup(server.Close)

	options := transcription.Options{Model: gladia.ModelSolaria1}
	if err := options.Validate(); err != nil {
		t.Fatal(err)
	}
	model, err := gladia.NewAudioTranscriptionModel(t.Context(), gladia.AudioTranscriptionModelConfig{
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

	audio, err := media.NewBytes("audio/mpeg", []byte("FAKE-AUDIO"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := transcription.NewRequest(audio)
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
	if !strings.Contains(err.Error(), "transcending") {
		t.Fatalf("Call() = %v, want an error naming the status", err)
	}
}
