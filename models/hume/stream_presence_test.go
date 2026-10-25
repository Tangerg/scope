package hume_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/hume"
)

func TestAudioChunkRequiresNativeOrderAndCompletion(t *testing.T) {
	for _, fields := range []string{`"is_last_chunk":true`, `"chunk_index":null,"is_last_chunk":true`, `"chunk_index":0`, `"chunk_index":0,"is_last_chunk":null`, `"chunk_index":0,"is_last_chunk":true`} {
		t.Run(fields, func(t *testing.T) {
			body := `{"type":"audio","audio":"YXVkaW8=","audio_format":"mp3","generation_id":"g1","request_id":"r1","snippet_id":"s1",` + fields + `}`
			server := modeltest.JSONServer(http.StatusOK, body)
			defer server.Close()
			model, err := hume.NewStreamingSpeechModel(t.Context(), hume.SpeechModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: speech.Options{Model: "1"}})
			if err != nil {
				t.Fatal(err)
			}
			request, err := speech.NewRequest("hello")
			if err != nil {
				t.Fatal(err)
			}
			var terminal error
			chunks := 0
			for response, err := range model.Stream(t.Context(), request) {
				if err != nil {
					terminal = err
					continue
				}
				if response != nil {
					chunks++
				}
			}
			if fields != `"chunk_index":0,"is_last_chunk":true` {
				if !errors.Is(terminal, speech.ErrInvalidResponse) || chunks != 0 {
					t.Fatalf("error=%v chunks=%d", terminal, chunks)
				}
				return
			}
			if terminal != nil || chunks != 1 {
				t.Fatalf("error=%v chunks=%d", terminal, chunks)
			}
		})
	}
}
