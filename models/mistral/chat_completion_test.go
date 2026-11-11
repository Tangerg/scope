package mistral_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/mistral"
)

type chatCompletionBody struct {
	reader   io.Reader
	read     int64
	closed   int
	closeErr error
}

func (c *chatCompletionBody) Read(buffer []byte) (int, error) {
	n, err := c.reader.Read(buffer)
	c.read += int64(n)
	return n, err
}

func (c *chatCompletionBody) Close() error { c.closed++; return c.closeErr }

type chatCompletionTransport struct {
	reader   func() io.Reader
	closeErr error
	status   int
	body     *chatCompletionBody
}

func (c *chatCompletionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		_ = request.Body.Close()
	}
	c.body = &chatCompletionBody{reader: c.reader(), closeErr: c.closeErr}
	return &http.Response{StatusCode: c.status, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: c.body, Request: request}, nil
}

type cancelAtCompletion struct {
	reader *strings.Reader
	cancel context.CancelFunc
}

func (c *cancelAtCompletion) Read(buffer []byte) (int, error) {
	n, err := c.reader.Read(buffer)
	if c.reader.Len() == 0 {
		c.cancel()
	}
	return n, err
}

func newCompletionChat(ctx context.Context, client *http.Client) (chat.Model, error) {
	return mistral.NewChat(ctx, mistral.ChatConfig{APIKey: "test", BaseURL: "https://api.scope.test", HTTPClient: client, DefaultOptions: chat.Options{Model: "test-model"}})
}

func chatCompletionWire(t *testing.T) string {
	t.Helper()
	return "data: {\"id\":\"m\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}]}\n\ndata: {\"id\":\"m\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
}

func TestChatCompletionRequiresSuccessfulTransport(t *testing.T) {
	closeFault := errors.New("response close failed")
	for _, test := range []struct {
		name     string
		closeErr error
		cancel   bool
	}{
		{name: "success"},
		{name: "final cancellation", cancel: true},
		{name: "close failure", closeErr: closeFault},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				transport := &chatCompletionTransport{status: http.StatusOK, closeErr: test.closeErr, reader: func() io.Reader {
					reader := strings.NewReader(chatCompletionWire(t))
					if test.cancel {
						return &cancelAtCompletion{reader: reader, cancel: cancel}
					}
					return reader
				}}
				model, err := newCompletionChat(t.Context(), &http.Client{Transport: transport})
				if err != nil {
					t.Fatal(err)
				}
				request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
				wantErr := test.closeErr
				if test.cancel {
					wantErr = context.Canceled
				}
				if stream {
					failures, finished := 0, 0
					for delta, streamErr := range model.(chat.Streamer).Stream(ctx, request) {
						if streamErr != nil {
							err = streamErr
							failures++
						}
						if delta != nil && delta.FinishReason != "" {
							finished++
						}
					}
					if wantErr == nil && (failures != 0 || finished != 1) || wantErr != nil && (failures != 1 || finished != 0) {
						t.Fatalf("failures=%d finished=%d error=%v", failures, finished, err)
					}
				} else {
					response, callErr := model.Call(ctx, request)
					err = callErr
					if wantErr == nil && (response == nil || response.Text() != "answer") || wantErr != nil && response != nil {
						t.Fatalf("response=%v error=%v", response, err)
					}
				}
				if wantErr == nil && err != nil || wantErr != nil && (!errors.Is(err, wantErr) || invocation.ClassifyFailure(err) != invocation.FailureUnknown) {
					t.Fatalf("stream=%t error=%v want=%v", stream, err, wantErr)
				}
				if transport.body == nil || transport.body.closed != 1 {
					t.Fatalf("body=%+v", transport.body)
				}
			}
		})
	}

	t.Run("consumer stop", func(t *testing.T) {
		transport := &chatCompletionTransport{status: http.StatusOK, closeErr: closeFault, reader: func() io.Reader { return strings.NewReader(chatCompletionWire(t)) }}
		model, err := newCompletionChat(t.Context(), &http.Client{Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		for delta, streamErr := range model.(chat.Streamer).Stream(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}) {
			if streamErr != nil || delta == nil {
				t.Fatalf("first delta=%v error=%v", delta, streamErr)
			}
			break
		}
		if transport.body == nil || transport.body.closed != 1 {
			t.Fatalf("body=%+v", transport.body)
		}
	})

}
