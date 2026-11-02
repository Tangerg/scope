package protocol_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

func TestChatRejectsNativeBodyOverridesBeforeIO(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, body := range []string{
			`{"tools":[{"codeExecution":{}}]}`,
			`{"contents":[{"role":"user","parts":[{"text":"replaced"}]}]}`,
			`{"generationConfig":{"temperature":0.75}}`,
		} {
			t.Run(provider+"/"+body, func(t *testing.T) {
				var calls atomic.Int32
				model := newNativeContractChat(t, provider, func(writer http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					writer.WriteHeader(http.StatusBadRequest)
				})
				request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
				if err := request.Options.Extensions.Set(provider+"/request", json.RawMessage(`{"httpOptions":{"extraBody":`+body+`}}`)); err != nil {
					t.Fatal(err)
				}
				if response, err := model.Call(t.Context(), request); response != nil || corechat.ClassifyFailure(err) != corechat.CallFailureNotExecuted {
					t.Fatalf("Call: response=%#v error=%v", response, err)
				}
				errorsReported := 0
				for delta, err := range model.Stream(t.Context(), request) {
					if delta != nil || corechat.ClassifyFailure(err) != corechat.CallFailureNotExecuted {
						t.Fatalf("Stream: delta=%#v error=%v", delta, err)
					}
					errorsReported++
				}
				if errorsReported != 1 || calls.Load() != 0 {
					t.Fatalf("stream errors=%d HTTP calls=%d", errorsReported, calls.Load())
				}
			})
		}
	}
}

func TestSpeechRejectsCodeExecution(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, streaming := range []bool{false, true} {
			for _, test := range generationCodeCases() {
				t.Run(fmt.Sprintf("%s/streaming=%t/%s", provider, streaming, test.name), func(t *testing.T) {
					var calls atomic.Int32
					config := protocol.SpeechModelConfig{
						Provider: provider, Client: newNativeContractClient(t, provider, test.handler(&calls)),
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
					request, err := speech.NewRequest("hello")
					if err != nil {
						t.Fatal(err)
					}
					wantErr := speech.ErrInvalidResponse
					if test.extension != "" {
						wantErr = errors.ErrUnsupported
						if err := request.Options.Extensions.Set(provider+"/speech_request", json.RawMessage(test.extension)); err != nil {
							t.Fatal(err)
						}
					}
					if response, err := model.Call(t.Context(), request); response != nil || !errors.Is(err, wantErr) {
						t.Fatalf("Call: response=%#v error=%v, want %v", response, err, wantErr)
					}
					if streaming {
						errorsReported := 0
						for response, err := range model.(speech.Streamer).Stream(t.Context(), request) {
							if response != nil || !errors.Is(err, wantErr) {
								t.Fatalf("Stream: response=%#v error=%v, want %v", response, err, wantErr)
							}
							errorsReported++
						}
						if errorsReported != 1 {
							t.Fatalf("stream errors=%d", errorsReported)
						}
					}
					if test.extension != "" && calls.Load() != 0 {
						t.Fatalf("unsupported code execution made %d HTTP calls", calls.Load())
					}
				})
			}
		}
	}
}

func TestTranscriptionRejectsCodeExecution(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, test := range generationCodeCases() {
			t.Run(provider+"/"+test.name, func(t *testing.T) {
				var calls atomic.Int32
				model, err := protocol.NewTranscriptionModel(t.Context(), protocol.TranscriptionModelConfig{
					Provider: provider, Client: newNativeContractClient(t, provider, test.handler(&calls)),
					DefaultOptions: transcription.Options{Model: protocol.ModelGemini36Flash},
				})
				if err != nil {
					t.Fatal(err)
				}
				audio, err := media.NewBytes("audio/mpeg", []byte("test-audio"))
				if err != nil {
					t.Fatal(err)
				}
				request, err := transcription.NewRequest(audio)
				if err != nil {
					t.Fatal(err)
				}
				wantErr := transcription.ErrInvalidResponse
				if test.extension != "" {
					wantErr = errors.ErrUnsupported
					if err := request.Options.Extensions.Set(provider+"/transcription_request", json.RawMessage(test.extension)); err != nil {
						t.Fatal(err)
					}
				}
				if response, err := model.Call(t.Context(), request); response != nil || !errors.Is(err, wantErr) {
					t.Fatalf("Call: response=%#v error=%v, want %v", response, err, wantErr)
				}
				if test.extension != "" && calls.Load() != 0 {
					t.Fatalf("unsupported code execution made %d HTTP calls", calls.Load())
				}
			})
		}
	}
}

type generationCodeCase struct {
	name      string
	extension string
	part      string
}

func (g generationCodeCase) handler(calls *atomic.Int32) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		parts := `{"text":"transcript"},{"inlineData":{"mimeType":"audio/L16;rate=24000","data":"AQI="}}`
		if g.part != "" {
			parts += "," + g.part
		}
		body := `{"candidates":[{"content":{"parts":[` + parts + `]},"finishReason":"STOP"}]}`
		if strings.HasSuffix(request.URL.Path, ":streamGenerateContent") {
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(writer, "data: %s\n\n", body)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, body)
	}
}

func generationCodeCases() []generationCodeCase {
	return []generationCodeCase{
		{name: "request object", extension: `{"tools":[{"codeExecution":{}}]}`},
		{name: "request null", extension: `{"tools":[{"codeExecution":null}]}`},
		{name: "executable code", part: `{"executableCode":{"language":"PYTHON","code":"print(2)"}}`},
		{name: "execution result", part: `{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"2"}}`},
	}
}
