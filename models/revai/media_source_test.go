package revai_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/revai"
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
