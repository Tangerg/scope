package gladia_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/gladia"
)

func TestAudioTranscriptionModel_Call_Mock(t *testing.T) {
	var polls modeltest.PollCounter

	srv := modeltest.MuxServer(
		modeltest.Route{Method: "POST", Contains: "/upload", Handle: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"audio_url":"https://cdn.test/audio.bin","audio_metadata":{"id":"a1"}}`))
		}},
		modeltest.Route{Method: "POST", Contains: "/pre-recorded", Handle: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"job-1","result_url":"/v2/pre-recorded/job-1"}`))
		}},
		modeltest.Route{Method: "GET", Contains: "/pre-recorded/", Handle: func(w http.ResponseWriter, r *http.Request) {
			n := polls.Inc()
			status := "processing"
			text := ""
			if n >= 2 {
				status = "done"
				text = "hello world"
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"job-1","status":"` + status + `","result":{"transcription":{"full_transcript":"` + text + `","languages":["en"]}}}`))
		}},
	)
	t.Cleanup(srv.Close)

	opts := transcription.Options{Model: gladia.ModelSolaria1}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := gladia.NewAudioTranscriptionModel(t.Context(), gladia.AudioTranscriptionModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
		PollInterval:   10 * time.Millisecond,
		PollTimeout:    5 * time.Second,
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
	if out.Output == nil || out.Output.Text != "hello world" {
		t.Fatalf("text = %v; want 'hello world'", out.Output)
	}
}
