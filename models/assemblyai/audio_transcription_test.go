package assemblyai_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/assemblyai"
)

func TestAudioTranscriptionModel_Call_Mock(t *testing.T) {
	var polls modeltest.PollCounter

	srv := modeltest.MuxServer(
		modeltest.Route{Method: "POST", Contains: "/upload", Handle: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"upload_url":"https://cdn.test/audio.bin"}`))
		}},
		modeltest.Route{Method: "POST", Contains: "/transcript", Handle: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"job-1","status":"queued","audio_duration":0}`))
		}},
		modeltest.Route{Method: "GET", Contains: "/transcript/", Handle: func(w http.ResponseWriter, r *http.Request) {
			n := polls.Inc()
			status := "processing"
			text := ""
			if n >= 2 {
				status = "completed"
				text = "hello world"
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"job-1","status":"` + status + `","text":"` + text + `","confidence":0.95,"audio_duration":1}`))
		}},
	)
	t.Cleanup(srv.Close)

	opts := transcription.Options{Model: assemblyai.ModelUniversal3Point5Pro}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := assemblyai.NewAudioTranscriptionModel(t.Context(), assemblyai.AudioTranscriptionModelConfig{
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
