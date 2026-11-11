package blackforestlabs_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/blackforestlabs"
)

func TestImageOutcomeEvidence(t *testing.T) {
	for _, test := range []struct {
		name            string
		body            string
		cancel          bool
		httpFailure     bool
		downloadFailure bool
		kind            invocation.FailureKind
	}{
		{name: "error", body: `{"id":"job-1","status":"Error"}`, kind: invocation.FailureFailed},
		{name: "failed", body: `{"id":"job-1","status":"Failed"}`, kind: invocation.FailureFailed},
		{name: "content moderated", body: `{"id":"job-1","status":"Content Moderated"}`, kind: invocation.FailureFailed},
		{name: "request moderated", body: `{"id":"job-1","status":"Request Moderated"}`, kind: invocation.FailureFailed},
		{name: "different job failed", body: `{"id":"other","status":"Error"}`, kind: invocation.FailureUnknown},
		{name: "unknown status", body: `{"id":"job-1","status":"new_status"}`, kind: invocation.FailureUnknown},
		{name: "accepted task not found", body: `{"id":"job-1","status":"Task not found"}`, kind: invocation.FailureUnknown},
		{name: "canceled poll", cancel: true, kind: invocation.FailureUnknown},
		{name: "poll HTTP failure", httpFailure: true, kind: invocation.FailureUnknown},
		{name: "success", body: `{"id":"job-1","status":"Ready","result":{"sample":"https://blackforestlabs.test/output.png"}}`},
		{name: "completed generation download failed", body: `{"id":"job-1","status":"Ready","result":{"sample":"https://blackforestlabs.test/output.png"}}`, downloadFailure: true, kind: invocation.FailureUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client := &http.Client{Transport: imageOutcomeTransport(func(request *http.Request) (*http.Response, error) {
				body := test.body
				contentType := "application/json"
				statusCode := http.StatusOK
				switch {
				case request.Method == http.MethodPost:
					body = `{"id":"job-1","polling_url":"https://blackforestlabs.test/get_result?id=job-1"}`
				case strings.HasSuffix(request.URL.Path, "/output.png"):
					if test.downloadFailure {
						return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: io.ErrUnexpectedEOF}
					}
					body, contentType = "PNG", "image/png"
				default:
					if test.cancel {
						cancel()
						return nil, ctx.Err()
					}
					if test.httpFailure {
						statusCode, body = http.StatusBadRequest, `{"error":"poll unavailable"}`
					}
				}
				return &http.Response{StatusCode: statusCode, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})}
			model, err := blackforestlabs.NewImageModel(t.Context(), blackforestlabs.ImageModelConfig{
				APIKey: "test", BaseURL: "https://blackforestlabs.test", HTTPClient: client,
				OutputOrigins: []string{"https://blackforestlabs.test"},

				DefaultOptions: image.Options{Model: blackforestlabs.ModelFlux11Pro},
				Polling:        invocation.Polling{Interval: time.Millisecond, Timeout: time.Second},
			})
			if err != nil {
				t.Fatal(err)
			}
			request, err := image.NewRequest("image")
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(ctx, request)
			if test.kind == invocation.FailureNone {
				if err != nil || response == nil || string(response.First().Media.Source.Bytes) != "PNG" {
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
			if test.downloadFailure && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("download diagnostic was lost: %v", err)
			}
		})
	}
}

type imageOutcomeTransport func(*http.Request) (*http.Response, error)

func (i imageOutcomeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close()
	}
	if request.URL.Host != "blackforestlabs.test" {
		return nil, errors.New("unexpected external request")
	}
	return i(request)
}
