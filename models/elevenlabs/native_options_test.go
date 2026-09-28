package elevenlabs_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/elevenlabs"
)

func TestSpeechQueryControlsAndBody(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, logging := range []*bool{nil, new(false), new(true)} {
			loggingName := "omitted"
			if logging != nil {
				loggingName = fmt.Sprint(*logging)
			}
			t.Run(fmt.Sprintf("stream=%t/logging=%s", streaming, loggingName), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					want := ""
					if logging != nil {
						want = fmt.Sprint(*logging)
					}
					if got := r.URL.Query().Get("enable_logging"); got != want {
						t.Errorf("enable_logging=%q want %q", got, want)
					}
					if got := r.URL.Query().Get("optimize_streaming_latency"); got != "0" {
						t.Errorf("latency=%q", got)
					}
					var body map[string]any
					if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
						t.Error(err)
					}
					if _, ok := body["enable_logging"]; ok {
						t.Error("query control leaked into body")
					}
					if _, ok := body["seed"]; ok {
						t.Error("absent body parameter encoded as null")
					}
					if _, ok := body["optimize_streaming_latency"]; ok {
						t.Error("query control leaked into body")
					}
					if body["text"] != "private message" || body["model_id"] != "eleven_multilingual_v2" {
						t.Errorf("body=%v", body)
					}
					if value, ok := body["previous_request_ids"].([]any); !ok || len(value) != 0 {
						t.Errorf("explicit empty IDs=%v", body["previous_request_ids"])
					}
					w.Header().Set("Content-Type", "audio/mpeg")
					fmt.Fprint(w, "audio")
				}))
				defer server.Close()
				opts := speech.Options{Model: "eleven_multilingual_v2", Voice: "voice-id"}
				extension := map[string]any{"optimize_streaming_latency": 0, "previous_request_ids": []string{}}
				if logging != nil {
					extension["enable_logging"] = *logging
				}
				if err := opts.Extensions.Set(elevenlabs.SpeechRequestExtensionKey, extension); err != nil {
					t.Fatal(err)
				}
				model, err := elevenlabs.NewAudioTTSModel(t.Context(), elevenlabs.AudioTTSModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
				if err != nil {
					t.Fatal(err)
				}
				request := &speech.Request{Text: "private message"}
				if streaming {
					for _, err := range model.Stream(t.Context(), request) {
						if err != nil {
							t.Fatal(err)
						}
					}
				} else {
					if _, err := model.Call(t.Context(), request); err != nil {
						t.Fatal(err)
					}
				}
				if calls != 1 {
					t.Errorf("calls=%d", calls)
				}
			})
		}
	}
}

func TestOfficialTranscriptionOptionsReachForm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		for field, want := range map[string]string{"diarize": "true", "num_speakers": "2", "keyterms": `["Scope"]`, "tag_audio_events": "false", "no_verbatim": "false"} {
			if got := r.FormValue(field); got != want {
				t.Errorf("%s=%q want %q", field, got, want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text":"spoken text","language_code":"en"}`)
	}))
	defer server.Close()
	opts := transcription.Options{Model: elevenlabs.ModelScribeV2}
	if err := opts.Extensions.Set(elevenlabs.TranscriptionRequestExtensionKey, map[string]any{"diarize": true, "num_speakers": 2, "keyterms": []string{"Scope"}, "tag_audio_events": false, "no_verbatim": false}); err != nil {
		t.Fatal(err)
	}
	model, err := elevenlabs.NewAudioTranscriptionModel(t.Context(), elevenlabs.AudioTranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
	if err != nil {
		t.Fatal(err)
	}
	audio, err := media.NewBytes("audio/mpeg", []byte("audio"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), &transcription.Request{Audio: audio}); err != nil {
		t.Fatal(err)
	}
}
