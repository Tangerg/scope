package gladia_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/gladia"
)

func TestTranscriptionReportsProviderFailureCode(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "integer", body: `{"id":"job-1","status":"error","error_code":500}`, want: "gladia: transcription failed with error code 500"},
		{name: "null", body: `{"id":"job-1","status":"error","error_code":null}`, want: "gladia: transcription failed"},
		{name: "missing", body: `{"id":"job-1","status":"error"}`, want: "gladia: transcription failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: transcriptionFailureTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				body := test.body
				if request.Method == http.MethodPost {
					body = `{"id":"job-1"}`
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})}
			model, err := gladia.NewTranscriptionModel(t.Context(), gladia.TranscriptionModelConfig{APIKey: "test-key", BaseURL: "https://gladia.test/v2", HTTPClient: client, DefaultOptions: transcription.Options{Model: gladia.ModelSolaria1}})
			if err != nil {
				t.Fatal(err)
			}
			audio, err := media.NewURI("audio/mpeg", "https://audio.test/input.mp3")
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &transcription.Request{Audio: audio})
			if response != nil || err == nil || err.Error() != test.want {
				t.Fatalf("Call = %#v, %v; want %q", response, err, test.want)
			}
			if calls != 2 {
				t.Fatalf("requests = %d; want one create and one terminal poll", calls)
			}
		})
	}
}

type transcriptionFailureTransport func(*http.Request) (*http.Response, error)

func (t transcriptionFailureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != "gladia.test" {
		return nil, errors.New("unexpected external request")
	}
	return t(request)
}
