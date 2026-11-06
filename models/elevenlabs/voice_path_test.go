package elevenlabs_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/elevenlabs"
)

func TestSpeechVoiceOwnsOnlyOnePathSegment(t *testing.T) {
	for _, test := range []struct {
		voice string
		valid bool
	}{
		{"JBFqnCBsd6RMkjVDRZzb", true}, {"voice-id_test", true},
		{"voice-id?enable_logging=false#", false}, {"voice#fragment", false},
		{"voice/segment", false}, {`voice\segment`, false}, {"voice%2fsegment", false},
		{".", false}, {"..", false}, {"%2e%2e", false},
	} {
		for _, defaults := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/defaults=%t", test.voice, defaults), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					calls.Add(1)
					if request.URL.EscapedPath() != "/v1/text-to-speech/"+test.voice+"/stream" || request.URL.RawQuery != "enable_logging=true&output_format=mp3_44100_128" {
						t.Errorf("request URI=%q", request.RequestURI)
					}
					writer.Header().Set("Content-Type", "audio/mpeg")
					_, _ = writer.Write([]byte("audio"))
				}))
				defer server.Close()
				options := speech.Options{Model: elevenlabs.ModelElevenV3, Voice: "default-voice", OutputFormat: "mp3_44100_128"}
				request, err := speech.NewRequest("hello")
				if err != nil {
					t.Fatal(err)
				}
				if defaults {
					options.Voice = test.voice
				} else {
					request.Options.Voice = test.voice
				}
				if setErr := options.Extensions.Set(elevenlabs.SpeechRequestExtensionKey, map[string]any{"enable_logging": true}); setErr != nil {
					t.Fatal(setErr)
				}
				model, err := elevenlabs.NewSpeechModel(t.Context(), elevenlabs.SpeechModelConfig{APIKey: "test", BaseURL: server.URL + "/v1", DefaultOptions: options})
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), request)
				if test.valid {
					if err != nil || string(response.Output.Audio) != "audio" || calls.Load() != 1 {
						t.Fatalf("response=%v error=%v requests=%d", response, err, calls.Load())
					}
				} else if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls.Load() != 0 {
					t.Fatalf("invalid voice response=%v error=%v requests=%d", response, err, calls.Load())
				}
			})
		}
	}
}
