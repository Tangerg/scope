package luma_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/luma"
)

func TestImageDownloadHTTPFailurePreservesSafeEvidence(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		requestID string
		wantID    string
	}{
		{name: "rate limited", status: http.StatusTooManyRequests, requestID: "asset-request-123", wantID: "asset-request-123"},
		{name: "service unavailable", status: http.StatusServiceUnavailable, requestID: "asset-request-456", wantID: "asset-request-456"},
		{name: "unsafe identity", status: http.StatusForbidden, requestID: "private request identity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			const privateBody = "private asset error body"
			client := &http.Client{Transport: imageOutcomeTransport(func(request *http.Request) (*http.Response, error) {
				status := http.StatusOK
				header := http.Header{"Content-Type": {"application/json"}}
				body := `{"id":"job-1","state":"queued"}`
				if request.Method == http.MethodGet {
					body = `{"id":"job-1","created_at":"2026-07-31T08:00:00Z","state":"completed","output":[{"type":"image","url":"https://luma.test/output.png"}]}`
				}
				if request.URL.Path == "/output.png" {
					status, body = test.status, privateBody
					header.Set("X-Request-Id", test.requestID)
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})}
			model, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{
				APIKey: "test", BaseURL: "https://luma.test", HTTPClient: client,
				OutputOrigins: []string{"https://luma.test"}, DefaultOptions: image.Options{Model: luma.ModelUni1},
			})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &image.Request{Prompt: "image"})
			failure, ok := errors.AsType[*httpclient.ResponseError](err)
			if response != nil || !ok || failure.HTTPStatus() != test.status || failure.RequestID() != test.wantID {
				t.Fatalf("Call = %+v, %v; want status=%d request_id=%q", response, err, test.status, test.wantID)
			}
			if invocation.ClassifyFailure(err) != invocation.FailureUnknown {
				t.Fatalf("download failure misrepresented execution: %v", err)
			}
			if strings.Contains(err.Error(), privateBody) || test.wantID == "" && strings.Contains(err.Error(), test.requestID) {
				t.Fatalf("unsafe diagnostic: %v", err)
			}
		})
	}
}
