package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/speech"
)

func TestSpeechStreamPreservesAudioBeforeReadError(t *testing.T) {
	for _, test := range []struct {
		name    string
		readErr error
		stop    bool
		want    []string
	}{
		{name: "last audio and EOF", readErr: io.EOF, want: []string{"audio"}},
		{name: "last audio and failure", readErr: io.ErrUnexpectedEOF, want: []string{"audio", "error"}},
		{name: "caller stops after audio", readErr: io.ErrUnexpectedEOF, stop: true, want: []string{"audio"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &speechStreamBody{readErr: test.readErr}
			model, err := NewAudioTTSModel(t.Context(), AudioTTSModelConfig{
				APIKey: "test-key", Provider: "openai",
				DefaultOptions: speech.Options{Model: "test-model", Voice: "test-voice"},
				HTTPClient:     &http.Client{Transport: speechStreamTransport{body: body, status: http.StatusOK}},
			})
			if err != nil {
				t.Fatal(err)
			}
			request, err := speech.NewRequest("hello")
			if err != nil {
				t.Fatal(err)
			}
			var events []string
			for response, streamErr := range model.Stream(t.Context(), request) {
				if streamErr != nil {
					if !errors.Is(streamErr, test.readErr) {
						t.Fatalf("stream error = %v, want %v", streamErr, test.readErr)
					}
					events = append(events, "error")
					continue
				}
				if validateErr := response.Validate(); validateErr != nil {
					t.Fatal(validateErr)
				}
				if string(response.Output.Audio) != "audio" {
					t.Fatalf("audio = %q", response.Output.Audio)
				}
				events = append(events, "audio")
				if test.stop {
					break
				}
			}
			if !slices.Equal(events, test.want) {
				t.Fatalf("events = %v, want %v", events, test.want)
			}
			if body.reads != 1 || !body.closed {
				t.Fatalf("reads = %d, closed = %t", body.reads, body.closed)
			}
		})
	}
}

type speechStreamTransport struct {
	body   *speechStreamBody
	status int
}

func (s speechStreamTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: s.status, Header: http.Header{"Content-Type": []string{"audio/mpeg"}},
		Body: s.body, Request: request,
	}, nil
}

type speechStreamBody struct {
	readErr error
	reads   int
	closed  bool
}

func (s *speechStreamBody) Read(buffer []byte) (int, error) {
	s.reads++
	if s.reads > 1 {
		return 0, io.EOF
	}
	return copy(buffer, "audio"), s.readErr
}

func (s *speechStreamBody) Close() error {
	s.closed = true
	return nil
}

func TestSpeechCallDiscardsPartialAudioAndClosesBody(t *testing.T) {
	for _, readErr := range []error{io.EOF, io.ErrUnexpectedEOF, context.Canceled} {
		t.Run(readErr.Error(), func(t *testing.T) {
			body := &speechStreamBody{readErr: readErr}
			model, err := NewAudioTTSModel(t.Context(), AudioTTSModelConfig{
				APIKey: "test-key", Provider: "openai",
				DefaultOptions: speech.Options{Model: "test-model", Voice: "test-voice"},
				HTTPClient:     &http.Client{Transport: speechStreamTransport{body: body, status: http.StatusOK}},
			})
			if err != nil {
				t.Fatal(err)
			}
			request, err := speech.NewRequest("hello")
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if errors.Is(readErr, io.EOF) {
				if err != nil || response == nil || string(response.Output.Audio) != "audio" {
					t.Fatalf("Call = %v, %v", response, err)
				}
			} else if response != nil || !errors.Is(err, readErr) {
				t.Fatalf("Call = %v, %v; want nil, %v", response, err, readErr)
			}
			if body.reads != 1 || !body.closed {
				t.Fatalf("reads = %d, closed = %v", body.reads, body.closed)
			}
		})
	}
}
