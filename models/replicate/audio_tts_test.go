package replicate_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/modeltest"
	tts "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/replicate"
)

func TestAudioTTSModel_Call_Mock(t *testing.T) {
	var polls modeltest.PollCounter

	// We register the audio download path on the same server — the
	// poll-success response will point at /audio.bin (same origin).
	var audioURL string
	srv := modeltest.MuxServer(
		modeltest.Route{Method: "POST", Contains: "/predictions", Handle: func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Version string         `json:"version"`
				Input   map[string]any `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			if body.Version != replicate.ModelXTTSV2 || body.Input["text"] != "hello world" || body.Input["speaker"] != "https://example.com/reference.wav" {
				t.Errorf("prediction request = %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"pred-tts","status":"starting","urls":{"get":"/v1/predictions/pred-tts"}}`))
		}},
		modeltest.Route{Method: "GET", Contains: "/predictions/", Handle: func(w http.ResponseWriter, r *http.Request) {
			n := polls.Inc()
			status := "processing"
			output := "null"
			if n >= 2 {
				status = "succeeded"
				output = fmt.Sprintf("%q", audioURL)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"pred-tts","status":"` + status + `","output":` + output + `}`))
		}},
		modeltest.Route{Method: "GET", Contains: "/audio.bin", Handle: func(w http.ResponseWriter, r *http.Request) {
			if authorization := r.Header.Get("Authorization"); authorization != "Bearer test-key" {
				t.Errorf("output download Authorization = %q", authorization)
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Write([]byte("FAKE-MP3"))
		}},
	)
	t.Cleanup(srv.Close)
	audioURL = srv.URL + "/audio.bin"

	opts := tts.Options{Model: replicate.ModelXTTSV2}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.Voice = "https://example.com/reference.wav"
	m, err := replicate.NewAudioTTSModel(t.Context(), replicate.AudioTTSModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		InputSchema:    replicate.XTTSV2SpeechInputSchema(),
		BaseURL:        srv.URL,
		PollInterval:   10 * time.Millisecond,
		PollTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := tts.NewRequest("hello world")
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Output == nil {
		t.Fatal("nil output")
	}
}
