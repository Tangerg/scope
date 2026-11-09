package revai_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/revai"
)

func TestTranscriptionOutcomeEvidence(t *testing.T) {
	nestedFailure := &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: io.ErrUnexpectedEOF}
	for _, source := range []string{"uri", "bytes"} {
		for _, test := range []struct {
			name          string
			body          string
			pollError     error
			cancel        bool
			downloadError error
			kind          invocation.FailureKind
		}{
			{name: "known failure", body: `{"id":"job-1","status":"failed","failure":"cannot transcribe"}`, kind: invocation.FailureFailed},
			{name: "different job failed", body: `{"id":"other","status":"failed"}`, kind: invocation.FailureUnknown},
			{name: "unknown status", body: `{"id":"job-1","status":"new_status"}`, kind: invocation.FailureUnknown},
			{name: "canceled poll", cancel: true, kind: invocation.FailureUnknown},
			{name: "nested operation did not execute", pollError: nestedFailure, kind: invocation.FailureUnknown},
			{name: "success", body: `{"id":"job-1","status":"transcribed"}`},
			{name: "transcript retrieval failed", body: `{"id":"job-1","status":"transcribed"}`, downloadError: nestedFailure, kind: invocation.FailureUnknown},
		} {
			t.Run(source+"/"+test.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				client := &http.Client{Transport: transcriptionOutcomeTransport(func(request *http.Request) (*http.Response, error) {
					body := test.body
					contentType := "application/json"
					if request.Method == http.MethodPost {
						body = `{"id":"job-1","status":"in_progress"}`
						if strings.HasSuffix(request.URL.Path, "/upload") {
							body = `{}`
						}
					} else if strings.HasSuffix(request.URL.Path, "/transcript") {
						if test.downloadError != nil {
							return nil, test.downloadError
						}
						body, contentType = "hello", "text/plain"
					} else {
						if test.cancel {
							cancel()
							return nil, ctx.Err()
						}
						if test.pollError != nil {
							return nil, test.pollError
						}
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
				})}
				model, err := revai.NewTranscriptionModel(t.Context(), revai.TranscriptionModelConfig{
					APIKey: "test", BaseURL: "https://revai.test", HTTPClient: client,
					DefaultOptions: transcription.Options{Model: revai.ModelMachine},
					PollInterval:   time.Millisecond, PollTimeout: time.Second,
				})
				if err != nil {
					t.Fatal(err)
				}
				audio, err := media.NewURI("audio/mpeg", "https://audio.test/input.mp3")
				if source == "bytes" {
					audio, err = media.NewBytes("audio/mpeg", []byte("audio"))
				}
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(ctx, &transcription.Request{Audio: audio})
				if test.kind == invocation.FailureNone {
					if err != nil || response == nil || response.Output.Text != "hello" {
						t.Fatalf("Call = %+v, %v", response, err)
					}
					return
				}
				if response != nil || invocation.ClassifyFailure(err) != test.kind {
					t.Fatalf("Call = %+v, %v; want %s", response, err, test.kind)
				}
				if test.cancel && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation was lost: %v", err)
				}
				if test.pollError != nil && !errors.Is(err, test.pollError) {
					t.Fatalf("nested diagnostic was lost: %v", err)
				}
			})
		}
	}
}

type transcriptionOutcomeTransport func(*http.Request) (*http.Response, error)

func (t transcriptionOutcomeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close()
	}
	if request.URL.Host != "revai.test" {
		return nil, errors.New("unexpected external request")
	}
	return t(request)
}
