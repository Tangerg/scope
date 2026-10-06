package bedrock

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	corechat "github.com/Tangerg/scope/core/chat"
)

func newStartedProtocolChunkAccumulator(t *testing.T, model string) *protocolChunkAccumulator {
	t.Helper()
	accumulator := newProtocolChunkAccumulator(model)
	if _, _, err := accumulator.add(messageStartEvent()); err != nil {
		t.Fatal(err)
	}
	return accumulator
}

func messageStartEvent() types.ConverseStreamOutput {
	return &types.ConverseStreamOutputMemberMessageStart{Value: types.MessageStartEvent{Role: types.ConversationRoleAssistant}}
}

func blockStopEvent(index int32) types.ConverseStreamOutput {
	return &types.ConverseStreamOutputMemberContentBlockStop{Value: types.ContentBlockStopEvent{ContentBlockIndex: aws.Int32(index)}}
}

func toolStartEvent(index int32) types.ConverseStreamOutput {
	return &types.ConverseStreamOutputMemberContentBlockStart{Value: types.ContentBlockStartEvent{
		ContentBlockIndex: aws.Int32(index),
		Start: &types.ContentBlockStartMemberToolUse{Value: types.ToolUseBlockStart{
			ToolUseId: aws.String("call-1"), Name: aws.String("lookup"),
		}},
	}}
}

func toolDeltaEvent(index int32, input string) types.ConverseStreamOutput {
	return &types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
		ContentBlockIndex: aws.Int32(index),
		Delta:             &types.ContentBlockDeltaMemberToolUse{Value: types.ToolUseBlockDelta{Input: aws.String(input)}},
	}}
}

func TestChatRejectsIncompleteOrCompetingStreamLifecycles(t *testing.T) {
	start := messageStartEvent()
	text := textDeltaEvent("answer")
	stop := messageStopEvent(types.StopReasonEndTurn)
	footer := &types.ConverseStreamOutputMemberMetadata{Value: types.ConverseStreamMetadataEvent{
		Usage: &types.TokenUsage{InputTokens: aws.Int32(1), OutputTokens: aws.Int32(2)},
	}}
	for _, test := range []struct {
		name   string
		events []types.ConverseStreamOutput
		valid  bool
	}{
		{name: "text opens on first delta", events: []types.ConverseStreamOutput{start, text, blockStopEvent(0), stop, footer}, valid: true},
		{name: "tool opens on start", events: []types.ConverseStreamOutput{start, toolStartEvent(0), toolDeltaEvent(0, "{}"), blockStopEvent(0), messageStopEvent(types.StopReasonToolUse), footer}, valid: true},
		{name: "missing message start", events: []types.ConverseStreamOutput{text, blockStopEvent(0), stop, footer}},
		{name: "duplicate message start", events: []types.ConverseStreamOutput{start, text, blockStopEvent(0), start, stop, footer}},
		{name: "wrong message role", events: []types.ConverseStreamOutput{&types.ConverseStreamOutputMemberMessageStart{Value: types.MessageStartEvent{Role: types.ConversationRoleUser}}, text, blockStopEvent(0), stop, footer}},
		{name: "text after block stop", events: []types.ConverseStreamOutput{start, text, blockStopEvent(0), text, stop, footer}},
		{name: "tool input after block stop", events: []types.ConverseStreamOutput{start, toolStartEvent(0), toolDeltaEvent(0, "{}"), blockStopEvent(0), toolDeltaEvent(0, "{}"), stop, footer}},
		{name: "duplicate block stop", events: []types.ConverseStreamOutput{start, text, blockStopEvent(0), blockStopEvent(0), stop, footer}},
		{name: "stop for unknown block", events: []types.ConverseStreamOutput{start, text, blockStopEvent(1), stop, footer}},
		{name: "missing block stop", events: []types.ConverseStreamOutput{start, text, stop, footer}},
		{name: "tool start replaces text", events: []types.ConverseStreamOutput{start, text, toolStartEvent(0), blockStopEvent(0), stop, footer}},
		{name: "text replaces tool", events: []types.ConverseStreamOutput{start, toolStartEvent(0), text, blockStopEvent(0), stop, footer}},
		{name: "duplicate metadata", events: []types.ConverseStreamOutput{start, text, blockStopEvent(0), stop, footer, footer}},
		{name: "metadata before stop", events: []types.ConverseStreamOutput{start, text, blockStopEvent(0), footer, stop}},
		{name: "empty stop reason", events: []types.ConverseStreamOutput{start, text, blockStopEvent(0), messageStopEvent(""), footer}},
		{name: "missing block index", events: []types.ConverseStreamOutput{start, &types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{Delta: &types.ContentBlockDeltaMemberText{Value: "answer"}}}, stop, footer}},
		{name: "negative block index", events: []types.ConverseStreamOutput{start, toolStartEvent(-1), toolDeltaEvent(-1, "{}"), blockStopEvent(-1), stop, footer}},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &Chat{defaults: corechat.Options{Model: "model"}, api: &scriptedConverse{events: test.events}}
			request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
			response, err := model.Call(t.Context(), request)
			if test.valid {
				if err != nil || response == nil || response.Metadata.Usage.OutputTokens != 2 {
					t.Fatalf("valid stream = %#v, %v", response, err)
				}
				return
			}
			if response != nil || !errors.Is(err, corechat.ErrInvalidResponse) {
				t.Fatalf("invalid stream = %#v, %v; want ErrInvalidResponse", response, err)
			}
			terminals := 0
			err = nil
			for delta, streamErr := range model.Stream(t.Context(), request) {
				if streamErr != nil {
					err = streamErr
					break
				}
				if delta.FinishReason != "" {
					terminals++
				}
			}
			if terminals != 0 || !errors.Is(err, corechat.ErrInvalidResponse) {
				t.Fatalf("invalid Stream: %d terminal deltas, error %v", terminals, err)
			}
		})
	}
}

func TestChatReportsUnmappedNativeContent(t *testing.T) {
	for name, event := range map[string]types.ConverseStreamOutput{
		"image start":       &types.ConverseStreamOutputMemberContentBlockStart{Value: types.ContentBlockStartEvent{ContentBlockIndex: aws.Int32(0), Start: &types.ContentBlockStartMemberImage{}}},
		"tool result start": &types.ConverseStreamOutputMemberContentBlockStart{Value: types.ContentBlockStartEvent{ContentBlockIndex: aws.Int32(0), Start: &types.ContentBlockStartMemberToolResult{}}},
		"citation delta":    &types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{ContentBlockIndex: aws.Int32(0), Delta: &types.ContentBlockDeltaMemberCitation{}}},
	} {
		t.Run(name, func(t *testing.T) {
			text := textDeltaEvent("answer")
			text.(*types.ConverseStreamOutputMemberContentBlockDelta).Value.ContentBlockIndex = aws.Int32(1)
			model := &Chat{defaults: corechat.Options{Model: "model"}, api: &scriptedConverse{events: []types.ConverseStreamOutput{
				messageStartEvent(), event, blockStopEvent(0), text, blockStopEvent(1), messageStopEvent(types.StopReasonEndTurn),
			}}}
			response, err := model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}})
			if response != nil || !errors.Is(err, errors.ErrUnsupported) {
				t.Fatalf("unmapped native content returned success: %#v, %v", response, err)
			}
		})
	}
}
