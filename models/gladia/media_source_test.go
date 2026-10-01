package gladia_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/gladia"
)

func TestTranscriptionUsesCoreAudioURI(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			posts++
			if strings.Contains(r.URL.Path, "upload") {
				t.Error("URI audio was uploaded")
			}
			var body map[string]any
			if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
				t.Error(err)
			}
			if body["audio_url"] != "https://example.test/audio.mp3" {
				t.Errorf("audio URI=%v", body["audio_url"])
			}
			fmt.Fprint(w, `{"id":"job-1"}`)

		} else {
			fmt.Fprint(w, `{"id":"job-1","status":"done","result":{"transcription":{"full_transcript":"transcribed"}}}`)
		}
	}))
	defer server.Close()
	opts := transcription.Options{Model: gladia.ModelSolaria1}
	model, err := gladia.NewTranscriptionModel(t.Context(), gladia.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
	if err != nil {
		t.Fatal(err)
	}
	audio, err := media.NewURI("audio/mpeg", "https://example.test/audio.mp3")
	if err != nil {
		t.Fatal(err)
	}
	result, err := model.Call(t.Context(), &transcription.Request{Audio: audio})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output.Text != "transcribed" || posts != 1 {
		t.Fatalf("result=%v posts=%d", result, posts)
	}
}

func TestTranscriptionRejectsNativeCoreFieldsBeforeIO(t *testing.T) {
	for name, extension := range map[string]any{"audio": map[string]any{"audio_url": "https://example.test/other.mp3"}, "model": map[string]any{"model": "other"}, "language": map[string]any{"language_config": map[string]any{"languages": []string{"en"}}}} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }))
			defer server.Close()
			opts := transcription.Options{Model: gladia.ModelSolaria1}
			if err := opts.Extensions.Set(gladia.RequestExtensionKey, extension); err != nil {
				t.Fatal(err)
			}
			model, err := gladia.NewTranscriptionModel(t.Context(), gladia.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
			if err != nil {
				t.Fatal(err)
			}
			audio, err := media.NewBytes("audio/mpeg", []byte("audio"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), &transcription.Request{Audio: audio}); err == nil || calls != 0 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}
