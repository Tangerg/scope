package elevenlabs_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/elevenlabs"
)

func TestSpeechVoiceOwnsOnlyOnePathSegment(t *testing.T) {
	for _, test := range []struct{ voice, segment string }{
		{"JBFqnCBsd6RMkjVDRZzb", "JBFqnCBsd6RMkjVDRZzb"}, {"voice-id_test", "voice-id_test"},
		{"voice-id?enable_logging=false#", "voice-id%3Fenable_logging=false%23"}, {"voice#fragment", "voice%23fragment"},
		{"voice/segment", "voice%2Fsegment"}, {`voice\segment`, "voice%5Csegment"}, {"voice%2fsegment", "voice%252fsegment"},
		{".", "%2E"}, {"..", "%2E%2E"}, {"%2e%2e", "%252e%252e"},
	} {
		for _, defaults := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/defaults=%t", test.voice, defaults), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					calls.Add(1)
					if request.URL.EscapedPath() != "/v1/text-to-speech/"+test.segment+"/stream" || request.URL.RawQuery != "enable_logging=true&output_format=mp3_44100_128" {
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
				if err != nil || string(response.Output.Audio) != "audio" || calls.Load() != 1 {
					t.Fatalf("response=%v error=%v requests=%d", response, err, calls.Load())
				}
			})
		}
	}
}
