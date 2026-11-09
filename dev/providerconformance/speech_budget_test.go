package providerconformance_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/deepgram"
	"github.com/Tangerg/scope/models/elevenlabs"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
	"github.com/Tangerg/scope/models/openai"
)

type speechBudgetCase struct {
	name     string
	body     string
	newModel func(context.Context, *http.Client, int64) (speech.Model, error)
}

func speechBudgetCases() []speechBudgetCase {
	const stream = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":\"audio/L16;rate=24000\",\"data\":\"YWJj\"}}]}}]}\n\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":\"audio/L16;rate=24000\",\"data\":\"ZGVm\"}}]},\"finishReason\":\"STOP\"}]}\n\n"
	const unary = `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/L16;rate=24000","data":"YWJj"}},{"inlineData":{"mimeType":"audio/L16;rate=24000","data":"ZGVm"}}]},"finishReason":"STOP"}]}`
	return []speechBudgetCase{
		{name: "openai", body: "abcdef", newModel: func(ctx context.Context, client *http.Client, limit int64) (speech.Model, error) {
			return openai.NewSpeechModel(ctx, openai.SpeechModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: speech.Options{Model: "tts-1", Voice: "alloy"}, MaxResponseBytes: limit})
		}},
		{name: "elevenlabs", body: "abcdef", newModel: func(ctx context.Context, client *http.Client, limit int64) (speech.Model, error) {
			return elevenlabs.NewSpeechModel(ctx, elevenlabs.SpeechModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: speech.Options{Model: "eleven_v3", Voice: "voice"}, MaxResponseBytes: limit})
		}},
		{name: "deepgram", body: "abcdef", newModel: func(ctx context.Context, client *http.Client, limit int64) (speech.Model, error) {
			return deepgram.NewSpeechModel(ctx, deepgram.SpeechModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: speech.Options{Model: "aura-2-thalia-en"}, MaxResponseBytes: limit})
		}},
		{name: "google streaming", body: stream, newModel: func(ctx context.Context, client *http.Client, limit int64) (speech.Model, error) {
			return google.NewStreamingSpeechModel(ctx, google.SpeechModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: speech.Options{Model: google.ModelGemini31FlashTTSPreview, Voice: "Kore"}, MaxResponseBytes: limit})
		}},
		{name: "vertex streaming", body: stream, newModel: func(ctx context.Context, client *http.Client, limit int64) (speech.Model, error) {
			return vertexai.NewStreamingSpeechModel(ctx, vertexai.SpeechModelConfig{Client: vertexai.ClientConfig{Project: "project", Location: "global", BaseURL: responseBudgetEndpoint, HTTPClient: client}, DefaultOptions: speech.Options{Model: google.ModelGemini31FlashTTSPreview, Voice: "Kore"}, MaxResponseBytes: limit})
		}},
		{name: "google unary", body: unary, newModel: func(ctx context.Context, client *http.Client, limit int64) (speech.Model, error) {
			return google.NewSpeechModel(ctx, google.SpeechModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: speech.Options{Model: google.ModelGemini25FlashPreviewTTS, Voice: "Kore"}, MaxResponseBytes: limit})
		}},
		{name: "vertex unary", body: unary, newModel: func(ctx context.Context, client *http.Client, limit int64) (speech.Model, error) {
			return vertexai.NewSpeechModel(ctx, vertexai.SpeechModelConfig{Client: vertexai.ClientConfig{Project: "project", Location: "global", BaseURL: responseBudgetEndpoint, HTTPClient: client}, DefaultOptions: speech.Options{Model: google.ModelGemini25FlashPreviewTTS, Voice: "Kore"}, MaxResponseBytes: limit})
		}},
	}
}

type speechBudgetReader struct{ reader io.Reader }

func (s speechBudgetReader) Read(buffer []byte) (int, error) {
	return s.reader.Read(buffer[:min(len(buffer), 2)])
}

func TestSpeechCallBoundsAggregateAudioAndLeavesStreamIncremental(t *testing.T) {
	for _, sample := range speechBudgetCases() {
		for _, limit := range []int64{5, 6, 7} {
			t.Run(fmt.Sprintf("%s/%d", sample.name, limit), func(t *testing.T) {
				transport := &responseBudgetTransport{status: http.StatusOK, contentType: "text/event-stream", reader: func(*http.Request) io.Reader { return speechBudgetReader{reader: strings.NewReader(sample.body)} }}
				model, err := sample.newModel(t.Context(), &http.Client{Transport: transport}, limit)
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), &speech.Request{Text: "hello"})
				if limit < 6 {
					if response != nil || !errors.Is(err, speech.ErrInvalidResponse) {
						t.Fatalf("response=%v error=%v", response, err)
					}
				} else if err != nil || response == nil || string(response.Output.Audio) != "abcdef" {
					t.Fatalf("response=%v error=%v", response, err)
				}
				if len(transport.bodies) != 1 || transport.bodies[0].closed != 1 {
					t.Fatalf("bodies=%+v", transport.bodies)
				}
				streamer, ok := model.(speech.Streamer)
				if !ok {
					return
				}
				var audio strings.Builder
				for response, err := range streamer.Stream(t.Context(), &speech.Request{Text: "hello"}) {
					if err != nil {
						t.Fatal(err)
					}
					audio.Write(response.Output.Audio)
				}
				if audio.String() != "abcdef" || len(transport.bodies) != 2 || transport.bodies[1].closed != 1 {
					t.Fatalf("audio=%q bodies=%+v", audio.String(), transport.bodies)
				}
			})
		}
	}
}

func TestSpeechByteLimitAdmissionAndCloseFailure(t *testing.T) {
	for _, sample := range speechBudgetCases() {
		t.Run(sample.name, func(t *testing.T) {
			fault := errors.New("audio transport close failed")
			transport := &responseBudgetTransport{status: http.StatusOK, contentType: "text/event-stream", closeErr: fault, reader: func(*http.Request) io.Reader { return strings.NewReader(sample.body) }}
			client := &http.Client{Transport: transport}
			if _, err := sample.newModel(t.Context(), client, -1); err == nil || len(transport.bodies) != 0 {
				t.Fatalf("negative limit error=%v", err)
			}
			model, err := sample.newModel(t.Context(), client, 6)
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &speech.Request{Text: "hello"})
			if response != nil || !errors.Is(err, fault) || len(transport.bodies) != 1 || transport.bodies[0].closed != 1 {
				t.Fatalf("response=%v error=%v bodies=%+v", response, err, transport.bodies)
			}
		})
	}
}
