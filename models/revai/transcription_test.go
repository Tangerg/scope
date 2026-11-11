package revai_test

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/revai"
)

func TestTranscriptionModel_Call_Mock(t *testing.T) {
	var polls atomic.Int32

	srv := modeltest.MuxServer(
		// Order matters: more specific routes first.
		modeltest.Route{Method: "GET", Contains: "/transcript", Handle: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("hello world\n"))
		}},
		modeltest.Route{Method: "POST", Contains: "/jobs", Handle: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"job-1","status":"in_progress","duration_seconds":1.0,"language":"en"}`))
		}},
		modeltest.Route{Method: "GET", Contains: "/jobs/", Handle: func(w http.ResponseWriter, r *http.Request) {
			// Skip the /transcript variant — already caught above.
			if strings.HasSuffix(r.URL.Path, "/transcript") {
				http.NotFound(w, r)
				return
			}
			n := polls.Add(1)
			status := "in_progress"
			if n >= 2 {
				status = "transcribed"
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"job-1","status":"` + status + `","duration_seconds":1.0,"language":"en"}`))
		}},
	)
	t.Cleanup(srv.Close)

	opts := transcription.Options{Model: revai.ModelMachine}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := revai.NewTranscriptionModel(t.Context(), revai.TranscriptionModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
		Polling:        invocation.Polling{Interval: 10 * time.Millisecond, Timeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}

	audio, _ := media.NewBytes("audio/mpeg", []byte("FAKE-AUDIO"))
	req, _ := transcription.NewRequest(audio)
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Output == nil || !strings.Contains(out.Output.Text, "hello world") {
		t.Fatalf("text = %q; want to contain 'hello world'", out.Output.Text)
	}
}
