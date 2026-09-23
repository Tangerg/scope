package modeltest_test

import (
	"context"
	"iter"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
)

type scriptedChat struct{}

func (scriptedChat) Call(context.Context, *chat.Request) (*chat.Response, error) {
	return &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop}, Metadata: &chat.ResponseMetadata{ID: "call"}}, nil
}

func (scriptedChat) Stream(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
	return func(yield func(*chat.ResponseDelta, error) bool) {
		yield(&chat.ResponseDelta{Metadata: &chat.ResponseMetadata{ID: "stream"}, FinishReason: chat.FinishReasonStop}, nil)
	}
}

var (
	_ chat.Model    = scriptedChat{}
	_ chat.Streamer = scriptedChat{}
)

func TestChatContract(t *testing.T) {
	callAsserted := false
	streamAsserted := false
	aggregatedAsserted := false
	modeltest.RunChatContract(t, modeltest.ChatContract{
		New: func(*testing.T) (chat.Model, chat.Streamer) {
			model := scriptedChat{}
			return model, model
		},
		Request: func(t *testing.T) *chat.Request {
			request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("hello")))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			return request
		},
		AssertCall: func(t *testing.T, response *chat.Response) {
			callAsserted = true
			if response.Metadata.ID != "call" {
				t.Fatalf("Call response ID = %q", response.Metadata.ID)
			}
		},
		AssertStream: func(t *testing.T, responses []*chat.ResponseDelta) {
			streamAsserted = true
			if len(responses) != 1 || responses[0].Metadata.ID != "stream" {
				t.Fatalf("Stream responses = %#v", responses)
			}
		},
		AssertAggregated: func(t *testing.T, response *chat.Response) {
			aggregatedAsserted = true
			if response.Metadata.ID != "stream" || response.Output.FinishReason != chat.FinishReasonStop {
				t.Fatalf("aggregated response = %#v", response)
			}
		},
	})

	if !callAsserted || !streamAsserted || !aggregatedAsserted {
		t.Fatalf("assert callbacks = call:%v stream:%v aggregated:%v", callAsserted, streamAsserted, aggregatedAsserted)
	}
}
