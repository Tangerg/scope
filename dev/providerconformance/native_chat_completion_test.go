package providerconformance_test

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
)

type chatCompletionBinding struct {
	name        string
	wire        string
	contentType string
	newModel    func(context.Context, *http.Client) (chat.Model, error)
}

func googleChatCompletionBindings(t *testing.T) []chatCompletionBinding {
	t.Helper()
	const googleWire = "data: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"answer\"}]}}]}\n\ndata: {\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\"}]}\n\n"
	return []chatCompletionBinding{
		{name: "google", wire: googleWire, newModel: func(ctx context.Context, client *http.Client) (chat.Model, error) {
			return google.NewChat(ctx, google.ChatConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: chat.Options{Model: "test-model"}})
		}},
		{name: "vertex", wire: googleWire, newModel: func(ctx context.Context, client *http.Client) (chat.Model, error) {
			return vertexai.NewChat(ctx, vertexai.ChatConfig{Client: vertexai.ClientConfig{Project: "project", Location: "global", BaseURL: responseBudgetEndpoint, HTTPClient: client}, DefaultOptions: chat.Options{Model: "test-model"}})
		}},
	}
}

type cancelAtStreamEnd struct {
	reader *strings.Reader
	cancel context.CancelFunc
}

func (c *cancelAtStreamEnd) Read(buffer []byte) (int, error) {
	n, err := c.reader.Read(buffer)
	if c.reader.Len() == 0 {
		c.cancel()
	}
	return n, err
}

func TestGoogleChatCompletionRejectsFinalCancellation(t *testing.T) {
	for _, sample := range googleChatCompletionBindings(t) {
		t.Run(sample.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				transport := &responseBudgetTransport{status: http.StatusOK, contentType: cmp.Or(sample.contentType, "text/event-stream"), reader: func(*http.Request) io.Reader {
					return &cancelAtStreamEnd{reader: strings.NewReader(sample.wire), cancel: cancel}
				}}
				model, err := sample.newModel(t.Context(), &http.Client{Transport: transport})
				if err != nil {
					t.Fatal(err)
				}
				request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
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
					if failures != 1 || finished != 0 {
						t.Fatalf("failures=%d finished=%d error=%v", failures, finished, err)
					}
				} else {
					response, callErr := model.Call(ctx, request)
					err = callErr
					if response != nil {
						t.Fatalf("canceled call succeeded: %+v", response)
					}
				}
				if !errors.Is(err, context.Canceled) || ctx.Err() != context.Canceled || invocation.ClassifyFailure(err) != invocation.FailureUnknown {
					t.Fatalf("error=%v context=%v", err, ctx.Err())
				}
				if len(transport.bodies) != 1 || transport.bodies[0].closed != 1 {
					t.Fatalf("bodies=%+v", transport.bodies)
				}
			}
		})
	}
}

func TestGoogleChatConsumerStopClosesOnce(t *testing.T) {
	for _, sample := range googleChatCompletionBindings(t) {
		t.Run(sample.name, func(t *testing.T) {
			transport := &responseBudgetTransport{status: http.StatusOK, contentType: cmp.Or(sample.contentType, "text/event-stream"), closeErr: errors.New("close failed"), reader: func(*http.Request) io.Reader { return strings.NewReader(sample.wire) }}
			model, err := sample.newModel(t.Context(), &http.Client{Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			for delta, err := range model.(chat.Streamer).Stream(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}) {
				if err != nil || delta == nil {
					t.Fatalf("first delta=%v error=%v", delta, err)
				}
				break
			}
			if len(transport.bodies) != 1 || transport.bodies[0].closed != 1 {
				t.Fatalf("bodies=%+v", transport.bodies)
			}
		})
	}
}
