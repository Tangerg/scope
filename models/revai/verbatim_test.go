package revai_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/revai"
)

type verbatimTransport struct {
	options map[string]json.RawMessage
}

func (v *verbatimTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close()
	}
	body := `{"id":"job-1","status":"transcribed"}`
	contentType := "application/json"
	if request.Method == http.MethodPost {
		options := io.Reader(request.Body)
		if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/") {
			reader, err := request.MultipartReader()
			if err != nil {
				return nil, err
			}
			for {
				part, err := reader.NextPart()
				if err != nil {
					return nil, err
				}
				if part.FormName() == "options" {
					options = part
					break
				}
			}
		}
		if err := jsonv2.UnmarshalRead(options, &v.options); err != nil {
			return nil, err
		}
	} else if strings.HasSuffix(request.URL.Path, "/transcript") {
		body, contentType = "transcribed", "text/plain"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func TestTranscriptionPreservesVerbatimPresence(t *testing.T) {
	for _, source := range []string{"URI", "upload"} {
		for _, location := range []string{"defaults", "request"} {
			for _, value := range []string{"", "false", "true"} {
				t.Run(source+"/"+location+"/"+value, func(t *testing.T) {
					transport := new(verbatimTransport)
					audio, err := media.NewBytes("audio/wav", []byte("audio"))
					if source == "URI" {
						audio, err = media.NewURI("audio/wav", "https://media.example/audio.wav")
					}
					if err != nil {
						t.Fatal(err)
					}
					request := &transcription.Request{Audio: audio}
					config := revai.TranscriptionModelConfig{APIKey: "key", BaseURL: "https://api.example", HTTPClient: &http.Client{Transport: transport}, DefaultOptions: transcription.Options{Model: revai.ModelMachine}}
					options := &config.DefaultOptions
					if location == "request" {
						options = &request.Options
					}
					if value != "" {
						if setErr := options.Extensions.Set(revai.RequestExtensionKey, json.RawMessage(`{"verbatim":`+value+`}`)); setErr != nil {
							t.Fatal(setErr)
						}
					}
					model, err := revai.NewTranscriptionModel(t.Context(), config)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := model.Call(t.Context(), request); err != nil {
						t.Fatal(err)
					}
					if got := string(transport.options["verbatim"]); got != value {
						t.Fatalf("verbatim = %q, want %q", got, value)
					}
				})
			}
		}
	}
}
