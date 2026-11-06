package hume_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/hume"
)

type terminalReader struct{ err error }

func (t terminalReader) Read([]byte) (int, error) { return 0, t.err }

type streamingTransport struct{ body io.ReadCloser }

func (s streamingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/x-ndjson"}}, Body: s.body, Request: request}, nil
}

func TestSpeechStreamPreservesFailuresContainingEOF(t *testing.T) {
	fault := errors.New("transport lost final bytes")
	for _, test := range []struct {
		name           string
		terminal, want error
	}{
		{name: "clean EOF", terminal: io.EOF},
		{name: "joined failure", terminal: errors.Join(io.EOF, fault), want: fault},
		{name: "wrapped EOF", terminal: fmt.Errorf("transport: %w", io.EOF), want: io.EOF},
		{name: "failure", terminal: fault, want: fault},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := `{"type":"audio","audio":"Zmlyc3Q=","audio_format":"mp3","chunk_index":0,"generation_id":"g1","request_id":"r1","snippet_id":"s1","is_last_chunk":true}` + "\n"
			body := io.NopCloser(io.MultiReader(strings.NewReader(event), terminalReader{err: test.terminal}))
			model, err := hume.NewStreamingSpeechModel(t.Context(), hume.SpeechModelConfig{APIKey: "test", DefaultOptions: speech.Options{Model: hume.ModelOctave2, Voice: "test"}, HTTPClient: &http.Client{Transport: streamingTransport{body: body}}})
			if err != nil {
				t.Fatal(err)
			}
			request, err := speech.NewRequest("hello")
			if err != nil {
				t.Fatal(err)
			}
			var terminal error
			chunks, failures := 0, 0
			for response, err := range model.Stream(t.Context(), request) {
				if err != nil {
					terminal = err
					failures++
				}
				if response != nil {
					chunks++
					if string(response.Output.Audio) != "first" {
						t.Errorf("audio=%q", response.Output.Audio)
					}
				}
			}
			wantFailures := 0
			if test.want != nil {
				wantFailures = 1
			}
			if chunks != 1 || failures != wantFailures || !errors.Is(terminal, test.want) {
				t.Fatalf("chunks=%d failures=%d error=%v", chunks, failures, terminal)
			}
		})
	}
}
