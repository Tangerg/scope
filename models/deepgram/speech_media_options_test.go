package deepgram_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/deepgram"
)

func TestSpeechRejectsInvalidMediaControlsBeforeIO(t *testing.T) {
	for _, test := range []struct {
		format string
		native string
	}{
		{"wav", `{"sample_rate":-1}`},
		{"wav", `{"sample_rate":0}`},
		{"mp3", `{"bit_rate":-1}`},
		{"mp3", `{"bit_rate":0}`},
		{"mp3", `{"sample_rate":24000}`},
		{"opus", `{"sample_rate":48000}`},
		{"aac", `{"sample_rate":22050}`},
		{"wav", `{"bit_rate":32000}`},
		{"mulaw", `{"bit_rate":32000}`},
		{"alaw", `{"bit_rate":32000}`},
		{"flac", `{"bit_rate":32000}`},
	} {
		for _, defaults := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/defaults=%t", test.format, test.native, defaults), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					calls++
					writer.Header().Set("Content-Type", "audio/mpeg")
					fmt.Fprint(writer, "audio")
				}))
				t.Cleanup(server.Close)
				options := speech.Options{Model: "aura-2-thalia-en", OutputFormat: test.format}
				request := &speech.Request{Text: "hello"}
				target := &request.Options
				if defaults {
					target = &options
				}
				if err := target.Extensions.Set(deepgram.SpeechRequestExtensionKey, json.RawMessage(test.native)); err != nil {
					t.Fatal(err)
				}
				model, err := deepgram.NewSpeechModel(t.Context(), deepgram.SpeechModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
				if err != nil {
					t.Fatal(err)
				}
				if response, err := model.Call(t.Context(), request); response != nil || err == nil || calls != 0 {
					t.Fatalf("Call = %#v, %v; requests=%d", response, err, calls)
				}
				var failure error
				for response, err := range model.Stream(t.Context(), request) {
					if response != nil {
						t.Fatalf("invalid media options produced audio: %#v", response)
					}
					failure = err
				}
				if failure == nil || calls != 0 {
					t.Fatalf("Stream error=%v, requests=%d", failure, calls)
				}
			})
		}
	}
}
