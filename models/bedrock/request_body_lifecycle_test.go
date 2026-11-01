package bedrock_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/bedrock"
)

func TestChatResponseSurvivesPendingRequestBodyCompletion(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", bedrockEventStreamContentType)
	for _, event := range []struct {
		kind    string
		payload string
	}{
		{"messageStart", `{"role":"assistant"}`},
		{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"ready"}}`},
		{"contentBlockStop", `{"contentBlockIndex":0}`},
		{"messageStop", `{"stopReason":"end_turn"}`},
		{"metadata", `{"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`},
	} {
		writeBedrockEvent(recorder, event.kind, []byte(event.payload))
	}
	transport := &pendingRequestTransport{response: recorder.Body.Bytes()}
	model, err := bedrock.NewChat(t.Context(), bedrock.ChatConfig{
		Region: "us-east-1", BaseURL: "https://bedrock.example.test",
		Credentials:    &bedrock.Credentials{AccessKeyID: "id", SecretAccessKey: "secret"},
		HTTPClient:     &http.Client{Transport: transport},
		DefaultOptions: corechat.Options{Model: "anthropic.claude-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), newBedrockBehaviorRequest(t))
	if err != nil {
		t.Fatalf("response after request body completion: %v", err)
	}
	if response == nil || response.Text() != "ready" {
		t.Fatalf("response = %#v; want ready", response)
	}
}

type pendingRequestTransport struct {
	response []byte
}

func (p *pendingRequestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body == nil || request.ContentLength <= 0 {
		return nil, errors.New("expected a bounded request payload")
	}
	if _, err := io.CopyN(io.Discard, request.Body, request.ContentLength); err != nil {
		return nil, err
	}
	// HTTP response headers can arrive before the transport finishes its final
	// request-body read. Smithy closes that body before starting event decoding.
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{bedrockEventStreamContentType}},
		Body: &pendingRequestResponse{
			ReadCloser:  io.NopCloser(bytes.NewReader(p.response)),
			requestBody: request.Body,
		},
	}, nil
}

type pendingRequestResponse struct {
	io.ReadCloser
	requestBody io.ReadCloser
	completed   bool
}

func (p *pendingRequestResponse) Read(buffer []byte) (int, error) {
	if !p.completed {
		p.completed = true
		if _, err := io.Copy(io.Discard, p.requestBody); err != nil {
			return 0, fmt.Errorf("complete request body: %w", err)
		}
	}
	return p.ReadCloser.Read(buffer)
}
