package revai_test

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
	"github.com/Tangerg/scope/models/revai"
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
		if strings.Contains(r.URL.Path, "transcript") {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "ok")
			return
		}
		fmt.Fprint(w, `{"id":"job-1","status":"transcribed"}`)
	}))
	defer server.Close()
	opts := transcription.Options{Model: revai.ModelMachine}
	if err := opts.Extensions.Set(revai.RequestExtensionKey, json.RawMessage(`{"notification_config":{"external_id":9007199254740993},"custom_vocabularies":[{"id":9007199254740993}]}`)); err != nil {
		t.Fatal(err)
	}
	model, err := revai.NewTranscriptionModel(t.Context(), revai.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
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
			source, ok := body["source_config"].(map[string]any)
			if !ok || source["url"] != "https://example.test/audio.mp3" {
				t.Errorf("source_config=%#v", source)
			}
			if _, exists := body["media_url"]; exists {
				t.Error("obsolete media_url present")
			}
			if headers, ok := source["auth_headers"].(map[string]any); !ok || headers["Authorization"] != "Bearer resource" {
				t.Errorf("authorization=%#v", source)
			}
			fmt.Fprint(w, `{"id":"job-1"}`)
		} else if strings.Contains(r.URL.Path, "transcript") {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "transcribed")
		} else {
			fmt.Fprint(w, `{"id":"job-1","status":"transcribed"}`)
		}
	}))
	defer server.Close()
	opts := transcription.Options{Model: revai.ModelMachine}
	if err := opts.Extensions.Set(revai.RequestExtensionKey, map[string]any{"source_config": map[string]any{"auth_headers": map[string]string{"Authorization": "Bearer resource"}}}); err != nil {
		t.Fatal(err)
	}
	model, err := revai.NewTranscriptionModel(t.Context(), revai.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
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
