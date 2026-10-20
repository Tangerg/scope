package protocol_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

func TestEmbeddingAdmitsEffectiveDimensionsAndNativeElements(t *testing.T) {
	for _, body := range []string{`{"embeddings":[null]}`, `{"embeddings":[{"values":[1,2]}]}`} {
		t.Run(body, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, body)
			defer server.Close()
			model, err := protocol.NewEmbeddingModel(t.Context(), protocol.EmbeddingModelConfig{Provider: "google", Client: protocol.ClientConfig{APIKey: "test", BaseURL: server.URL}, DefaultOptions: embedding.Options{Model: protocol.ModelGeminiEmbedding2, Dimensions: new(int64(128))}})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := embedding.NewRequest([]string{"text"})
			_, err = model.Call(t.Context(), request)
			if !errors.Is(err, embedding.ErrInvalidResponse) {
				t.Fatalf("invalid native embedding=%v", err)
			}
			if request.Options.Dimensions != nil {
				t.Fatal("Call mutated input options")
			}
		})
	}
}

func TestSpeechRejectsNullNativeObjectsBeforeSDKConversion(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, body := range []string{`{"candidates":[null]}`, `{"candidates":[{"finishReason":"STOP","content":{"parts":[null]}}]}`} {
			t.Run(body, func(t *testing.T) {
				content := body
				if streaming {
					content = "data: " + body + "\n\n"
				}
				server := modeltest.JSONServer(http.StatusOK, content)
				defer server.Close()
				name := protocol.ModelGemini25FlashPreviewTTS
				if streaming {
					name = protocol.ModelGemini31FlashTTSPreview
				}
				config := protocol.SpeechModelConfig{Provider: "google", Client: protocol.ClientConfig{APIKey: "test", BaseURL: server.URL}, DefaultOptions: speech.Options{Model: name}}
				request, _ := speech.NewRequest("text")
				if streaming {
					model, err := protocol.NewStreamingSpeechModel(t.Context(), config)
					if err != nil {
						t.Fatal(err)
					}
					failures := 0
					for response, err := range model.Stream(t.Context(), request) {
						if response != nil || err == nil {
							t.Fatalf("malformed frame produced %v, %v", response, err)
						}
						failures++
					}
					if failures != 1 {
						t.Fatalf("terminal errors=%d", failures)
					}
				} else {
					model, err := protocol.NewSpeechModel(t.Context(), config)
					if err != nil {
						t.Fatal(err)
					}
					if response, err := model.Call(t.Context(), request); response != nil || err == nil {
						t.Fatalf("malformed response=%v, %v", response, err)
					}
				}
			})
		}
	}
}

func TestTextCounterRejectsNegativeNativeCount(t *testing.T) {
	server := modeltest.JSONServer(http.StatusOK, `{"totalTokens":-1}`)
	defer server.Close()
	counter, err := protocol.NewTextCounter(t.Context(), protocol.TextCounterConfig{Model: "model", Client: protocol.ClientConfig{APIKey: "test", BaseURL: server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := counter.CountText(t.Context(), "text"); err == nil {
		t.Fatalf("negative count=%d", count)
	}
}
