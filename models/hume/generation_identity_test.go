package hume_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/hume"
)

func TestStreamBindsAllSnippetsToOneGeneration(t *testing.T) {
	const first = `{"type":"audio","audio":"YXVkaW8=","audio_format":"mp3","chunk_index":0,"generation_id":"g1","request_id":"r1","snippet_id":"s1","is_last_chunk":true}`
	second := strings.Replace(first, `"snippet_id":"s1"`, `"snippet_id":"s2"`, 1)
	for _, test := range []struct {
		name, second string
	}{
		{"another_request", strings.Replace(second, `"request_id":"r1"`, `"request_id":"r2"`, 1)},
		{"another_generation", strings.Replace(second, `"generation_id":"g1"`, `"generation_id":"g2"`, 1)},
		{"changed_audio_format", strings.Replace(second, `"audio_format":"mp3"`, `"audio_format":"wav"`, 1)},
		{"missing_audio_format", strings.Replace(second, `"audio_format":"mp3",`, "", 1)},
		{"unrequested_utterance", strings.Replace(second, `"is_last_chunk":true`, `"is_last_chunk":true,"utterance_index":1`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, first+"\n"+test.second+"\n")
			t.Cleanup(server.Close)
			model, err := hume.NewStreamingSpeechModel(t.Context(), hume.SpeechModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: speech.Options{Model: hume.ModelOctave1}})
			if err != nil {
				t.Fatal(err)
			}
			chunks, failures := 0, 0
			for response, err := range model.Stream(t.Context(), &speech.Request{Text: "hello"}) {
				if err != nil {
					failures++
					if !errors.Is(err, speech.ErrInvalidResponse) {
						t.Errorf("error=%v; want invalid response", err)
					}
				} else if response != nil {
					chunks++
				}
			}
			if chunks != 1 || failures != 1 {
				t.Fatalf("chunks=%d failures=%d", chunks, failures)
			}
		})
	}
}

func TestUnarySpeechRejectsUnrequestedGenerations(t *testing.T) {
	server := modeltest.JSONServer(http.StatusOK, `{"generations":[{"audio":"Zmlyc3Q="},{"audio":"c2Vjb25k"}]}`)
	t.Cleanup(server.Close)
	model, err := hume.NewSpeechModel(t.Context(), hume.SpeechModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: speech.Options{Model: hume.ModelOctave1}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), &speech.Request{Text: "hello"})
	if response != nil || !errors.Is(err, speech.ErrInvalidResponse) {
		t.Fatalf("response=%v error=%v", response, err)
	}
}
