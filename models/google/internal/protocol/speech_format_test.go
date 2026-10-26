package protocol_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

func TestSpeechRequiresOneAudioFormat(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, test := range []struct {
			name      string
			mimeTypes []string
			valid     bool
		}{
			{name: "missing MIME", mimeTypes: []string{""}},
			{name: "non-audio MIME", mimeTypes: []string{"image/png"}},
			{name: "changed encoding", mimeTypes: []string{"audio/L16;rate=24000", "audio/wav"}},
			{name: "changed sample rate", mimeTypes: []string{"audio/L16;rate=24000", "audio/L16;rate=16000"}},
			{name: "equivalent MIME", mimeTypes: []string{"audio/L16; rate=24000", "audio/l16;rate=24000"}, valid: true},
		} {
			t.Run(fmt.Sprintf("%s/streaming=%v", test.name, streaming), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					if streaming {
						writer.Header().Set("Content-Type", "text/event-stream")
						for _, mimeType := range test.mimeTypes {
							fmt.Fprintf(writer, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":%q,\"data\":\"YQ==\"}}]}}]}\n\n", mimeType)
						}
						fmt.Fprint(writer, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")
						return
					}
					writer.Header().Set("Content-Type", "application/json")
					fmt.Fprint(writer, `{"candidates":[{"finishReason":"STOP","content":{"parts":[`)
					for index, mimeType := range test.mimeTypes {
						if index > 0 {
							fmt.Fprint(writer, ",")
						}
						fmt.Fprintf(writer, `{"inlineData":{"mimeType":%q,"data":"YQ=="}}`, mimeType)
					}
					fmt.Fprint(writer, `]}}]}`)
				}))
				defer server.Close()
				config := protocol.SpeechModelConfig{
					Provider: "google", Client: protocol.ClientConfig{APIKey: "test", BaseURL: server.URL},
					DefaultOptions: speech.Options{Model: protocol.ModelGemini25FlashPreviewTTS},
				}
				var model speech.Model
				var err error
				if streaming {
					config.DefaultOptions.Model = protocol.ModelGemini31FlashTTSPreview
					model, err = protocol.NewStreamingSpeechModel(t.Context(), config)
				} else {
					model, err = protocol.NewSpeechModel(t.Context(), config)
				}
				if err != nil {
					t.Fatal(err)
				}
				request, err := speech.NewRequest("text")
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), request)
				if !test.valid {
					if response != nil || !errors.Is(err, speech.ErrInvalidResponse) {
						t.Fatalf("response=%v error=%v", response, err)
					}
					return
				}
				if err != nil || response == nil || len(response.Output.Audio) != len(test.mimeTypes) {
					t.Fatalf("response=%v error=%v", response, err)
				}
			})
		}
	}
}
