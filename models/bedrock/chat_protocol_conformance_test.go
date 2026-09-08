package bedrock

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
)

// scriptedConverse answers both Converse calls from a fixed script. Chat needs
// only these two, which is why it holds a narrow interface rather than the
// runtime client.
type scriptedConverse struct {
	output *bedrockruntime.ConverseOutput
	events []types.ConverseStreamOutput
}

func (s *scriptedConverse) converse(
	_ context.Context,
	_ *bedrockruntime.ConverseInput,
	_ ...func(*bedrockruntime.Options),
) (*bedrockruntime.ConverseOutput, error) {
	return s.output, nil
}

func (s *scriptedConverse) converseStream(
	_ context.Context,
	_ *bedrockruntime.ConverseStreamInput,
	_ ...func(*bedrockruntime.Options),
) (*bedrockruntime.ConverseStreamEventStream, error) {
	events := make(chan types.ConverseStreamOutput, len(s.events))
	for _, event := range s.events {
		events <- event
	}
	close(events)
	return bedrockruntime.NewConverseStreamEventStream(
		func(stream *bedrockruntime.ConverseStreamEventStream) {
			stream.Reader = &scriptedEventReader{events: events}
		},
	), nil
}

// scriptedEventReader is the reader the SDK documents as the seam for testing
// code that consumes an event stream.
type scriptedEventReader struct {
	events chan types.ConverseStreamOutput
}

func (s *scriptedEventReader) Events() <-chan types.ConverseStreamOutput { return s.events }
func (s *scriptedEventReader) Close() error                              { return nil }
func (s *scriptedEventReader) Err() error                                { return nil }

// This adapter owns its protocol rather than reaching a shared one, so nothing
// else pinned the Model and Streamer contract for it: that a call leaves the
// request untouched, that every delta is valid on its own, and that the deltas
// aggregate through [corechat.ResponseAccumulator] into a valid response.
func TestChat_CoreConformance(t *testing.T) {
	modeltest.ChatSuite{
		New: func(t *testing.T) (corechat.Model, corechat.Streamer) {
			t.Helper()
			adapter := &Chat{
				api: &scriptedConverse{
					output: scriptedConverseOutput(),
					events: scriptedConverseEvents(),
				},
				defaults: corechat.Options{Model: "anthropic.claude-test"},
			}
			return adapter, adapter
		},
		Request: func(t *testing.T) *corechat.Request {
			t.Helper()
			return &corechat.Request{
				Messages: []corechat.Message{
					corechat.NewSystemMessage("be brief"),
					corechat.NewUserMessage(corechat.NewTextPart("hello")),
				},
			}
		},
	}.Run(t)
}

func scriptedConverseOutput() *bedrockruntime.ConverseOutput {
	return &bedrockruntime.ConverseOutput{
		Output: &types.ConverseOutputMemberMessage{Value: types.Message{
			Role:    types.ConversationRoleAssistant,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hello there"}},
		}},
		StopReason: types.StopReasonEndTurn,
		Usage:      &types.TokenUsage{InputTokens: aws.Int32(9), OutputTokens: aws.Int32(3)},
	}
}

func scriptedConverseEvents() []types.ConverseStreamOutput {
	index := int32(0)
	return []types.ConverseStreamOutput{
		&types.ConverseStreamOutputMemberContentBlockStart{Value: types.ContentBlockStartEvent{
			ContentBlockIndex: &index,
		}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
			ContentBlockIndex: &index,
			Delta:             &types.ContentBlockDeltaMemberText{Value: "hello"},
		}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
			ContentBlockIndex: &index,
			Delta:             &types.ContentBlockDeltaMemberText{Value: " there"},
		}},
		&types.ConverseStreamOutputMemberMessageStop{Value: types.MessageStopEvent{
			StopReason: types.StopReasonEndTurn,
		}},
		&types.ConverseStreamOutputMemberMetadata{Value: types.ConverseStreamMetadataEvent{
			Usage: &types.TokenUsage{InputTokens: aws.Int32(9), OutputTokens: aws.Int32(3)},
		}},
	}
}
