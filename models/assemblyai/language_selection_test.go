package assemblyai_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/assemblyai"
)

func TestTranscriptionLanguageSelectionBeforeUpload(t *testing.T) {
	for _, test := range []struct {
		name, language, native string
		codes                  []string
		detection              *bool
		invalid                bool
	}{
		{name: "default", native: `{}`},
		{name: "single language", language: "en", native: `{}`},
		{name: "language set", native: `{"language_codes":["es","fr"]}`, codes: []string{"es", "fr"}},
		{name: "automatic", native: `{"language_detection":true}`, detection: new(true)},
		{name: "disabled detection", native: `{"language_detection":false}`, detection: new(false)},
		{name: "single language with disabled detection", language: "en", native: `{"language_detection":false}`, detection: new(false)},
		{name: "core and language set", language: "en", native: `{"language_codes":["es","fr"]}`, invalid: true},
		{name: "core and automatic", language: "en", native: `{"language_detection":true}`, invalid: true},
		{name: "set and automatic", native: `{"language_codes":["es","fr"],"language_detection":true}`, invalid: true},
		{name: "all modes", language: "en", native: `{"language_codes":["es","fr"],"language_detection":true}`, invalid: true},
	} {
		for _, defaults := range []bool{false, true} {
			for _, inline := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/defaults=%t/inline=%t", test.name, defaults, inline), func(t *testing.T) {
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
						calls.Add(1)
						writer.Header().Set("Content-Type", "application/json")
						switch {
						case request.URL.Path == "/upload":
							_, _ = fmt.Fprint(writer, `{"upload_url":"https://example.test/audio.mp3"}`)
						case request.Method == http.MethodPost:
							var body struct {
								Code      string   `json:"language_code"`
								Codes     []string `json:"language_codes"`
								Detection *bool    `json:"language_detection"`
							}
							if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
								t.Error(err)
							}
							if body.Code != test.language || !slices.Equal(body.Codes, test.codes) || (body.Detection == nil) != (test.detection == nil) || body.Detection != nil && *body.Detection != *test.detection {
								t.Errorf("wire language selection=%#v", body)
							}
							_, _ = fmt.Fprint(writer, `{"id":"transcript-1"}`)
						default:
							_, _ = fmt.Fprint(writer, `{"id":"transcript-1","status":"completed","text":"transcribed"}`)
						}
					}))
					defer server.Close()
					options := transcription.Options{Model: assemblyai.ModelUniversal3Point5Pro}
					audio, err := media.NewURI("audio/mpeg", "https://example.test/audio.mp3")
					if inline {
						audio, err = media.NewBytes("audio/mpeg", []byte("audio"))
					}
					if err != nil {
						t.Fatal(err)
					}
					request := &transcription.Request{Audio: audio}
					nativeOptions := &request.Options
					options.Language = test.language
					if defaults {
						nativeOptions = &options
						options.Language = ""
						request.Options.Language = test.language
					}
					if setErr := nativeOptions.Extensions.Set(assemblyai.RequestExtensionKey, json.RawMessage(test.native)); setErr != nil {
						t.Fatal(setErr)
					}
					model, err := assemblyai.NewTranscriptionModel(t.Context(), assemblyai.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
					if err != nil {
						t.Fatal(err)
					}
					response, err := model.Call(t.Context(), request)
					if test.invalid {
						if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls.Load() != 0 {
							t.Fatalf("invalid selection response=%v error=%v requests=%d", response, err, calls.Load())
						}
						return
					}
					wantCalls := int32(2)
					if inline {
						wantCalls++
					}
					if err != nil || response.Output.Text != "transcribed" || calls.Load() != wantCalls {
						t.Fatalf("response=%v error=%v requests=%d", response, err, calls.Load())
					}
				})
			}
		}
	}
}
