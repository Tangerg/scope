package bedrock_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/bedrock"
)

type repeatedCompletionByte byte

func (r repeatedCompletionByte) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = byte(r)
	}
	return len(buffer), nil
}

func TestSDKJSONResponsesAreBoundedBeforeDecoding(t *testing.T) {
	const limit = httpclient.DefaultMaxJSONResponseBytes
	for _, sample := range []struct {
		name     string
		statuses []int
		call     func(context.Context, *http.Client) error
	}{
		{name: "embedding", statuses: []int{http.StatusOK, http.StatusBadRequest}, call: func(ctx context.Context, client *http.Client) error {
			model, err := bedrock.NewEmbeddingModel(ctx, bedrock.EmbeddingModelConfig{Region: "us-east-1", BaseURL: "https://api.scope.test", HTTPClient: client, Credentials: &bedrock.Credentials{AccessKeyID: "id", SecretAccessKey: "secret"}, DefaultOptions: embedding.Options{Model: "amazon.titan-embed-text-v2:0"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &embedding.Request{Texts: []string{"hello"}})
			return err
		}},
		{name: "streaming error", statuses: []int{http.StatusBadRequest}, call: func(ctx context.Context, client *http.Client) error {
			model, err := newCompletionChat(ctx, client)
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}})
			return err
		}},
	} {
		for _, status := range sample.statuses {
			t.Run(sample.name+"/"+http.StatusText(status), func(t *testing.T) {
				payload := `{"embedding":[1,2],"inputTextTokenCount":1}`
				if status == http.StatusBadRequest {
					payload = `{"message":"oversized response","__type":"ValidationException"}`
				}
				transport := &chatCompletionTransport{status: status, contentType: "application/json", reader: func() io.Reader {
					return io.MultiReader(io.LimitReader(repeatedCompletionByte(' '), limit+1), strings.NewReader(payload))
				}}
				err := sample.call(t.Context(), &http.Client{Transport: transport})
				limitErr, limited := errors.AsType[*http.MaxBytesError](err)
				if !limited || limitErr.Limit != limit || invocation.ClassifyFailure(err) != invocation.FailureUnknown {
					t.Fatalf("error=%v", err)
				}
				if body := transport.body; body == nil || body.read != limit+1 || body.closed != 1 {
					t.Fatalf("body=%+v", body)
				}
			})
		}
	}
}
