package assemblyai_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/assemblyai"
)

func TestTranscriptionPreservesNativeIntegerPrecision(t *testing.T) {
	var captured string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			captured = string(body)
			fmt.Fprint(w, `{"id":"job-1"}`)
			return
		}
		fmt.Fprint(w, `{"id":"job-1","status":"completed","text":"ok"}`)
	}))
	defer server.Close()
	opts := transcription.Options{Model: assemblyai.ModelUniversal2}
	if err := opts.Extensions.Set(assemblyai.RequestExtensionKey, json.RawMessage(`{"speech_understanding":{"external_id":9007199254740993}}`)); err != nil {
		t.Fatal(err)
	}
	model, err := assemblyai.NewTranscriptionModel(t.Context(), assemblyai.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
	if err != nil {
		t.Fatal(err)
	}
	audio, err := media.NewURI("audio/mpeg", "https://example.test/audio.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), &transcription.Request{Audio: audio}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(captured, "9007199254740993") || strings.Contains(captured, "9007199254740992") {
		t.Fatalf("serialized request lost integer precision: %s", captured)
	}
}

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
			fmt.Fprint(w, `{"id":"job-1","status":"completed","text":"transcribed"}`)
		}
	}))
	defer server.Close()
	opts := transcription.Options{Model: assemblyai.ModelUniversal2}
	model, err := assemblyai.NewTranscriptionModel(t.Context(), assemblyai.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
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
