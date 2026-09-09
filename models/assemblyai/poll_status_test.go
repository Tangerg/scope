package assemblyai_test

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
	"github.com/Tangerg/scope/models/assemblyai"
)

// AssemblyAI documents four transcript statuses: queued, processing,
// completed and error.
// The poll loop waits only on the statuses documented as still moving, and
// reports anything else: continuing to poll is safe only for a state known to
// advance, so a status added later would otherwise arrive as this call's own
// timeout, whose obvious remedies -- retry, a longer deadline -- are both wrong.
func TestPollReportsAnUnrecognizedStatus(t *testing.T) {
	t.Parallel()

	server := modeltest.MuxServer(
		modeltest.Route{Method: "POST", Contains: "/upload", Handle: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"upload_url":"https://cdn.test/audio.bin"}`))
		}},
		modeltest.Route{Method: "POST", Contains: "/transcript", Handle: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"job-1","status":"queued"}`))
		}},
		modeltest.Route{Method: "GET", Contains: "/transcript/", Handle: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"job-1","status":"transcending"}`))
		}},
	)
	t.Cleanup(server.Close)

	options := transcription.Options{Model: assemblyai.ModelUniversal3Point5Pro}
	if err := options.Validate(); err != nil {
		t.Fatal(err)
	}
	model, err := assemblyai.NewAudioTranscriptionModel(t.Context(), assemblyai.AudioTranscriptionModelConfig{
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
