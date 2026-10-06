package bedrock

import (
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestReasoningBlocksRetainIdentityThroughHistory(t *testing.T) {
	for _, redacted := range []bool{false, true} {
		name := "signed"
		if redacted {
			name = "redacted"
		}
		t.Run(name, func(t *testing.T) {
			mapper := newStartedProtocolChunkAccumulator(t, "anthropic.claude-test")
			var accumulator corechat.ResponseAccumulator
			for index, text := range []string{"first", "second"} {
				values := []types.ReasoningContentBlockDelta{
					&types.ReasoningContentBlockDeltaMemberText{Value: text},
					&types.ReasoningContentBlockDeltaMemberSignature{Value: "sig-"},
					&types.ReasoningContentBlockDeltaMemberSignature{Value: text},
				}
				if redacted {
					values = []types.ReasoningContentBlockDelta{
						&types.ReasoningContentBlockDeltaMemberRedactedContent{Value: []byte("opaque-")},
						&types.ReasoningContentBlockDeltaMemberRedactedContent{Value: []byte(text)},
					}
				}
				for _, value := range values {
					delta, include, err := mapper.add(&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
						ContentBlockIndex: aws.Int32(int32(index)),
						Delta:             &types.ContentBlockDeltaMemberReasoningContent{Value: value},
					}})
					if err != nil || !include {
						t.Fatalf("reasoning delta = %v, %t", err, include)
					}
					if err := accumulator.Add(delta); err != nil {
						t.Fatal(err)
					}
				}
				if _, _, err := mapper.add(&types.ConverseStreamOutputMemberContentBlockStop{Value: types.ContentBlockStopEvent{
					ContentBlockIndex: aws.Int32(int32(index)),
				}}); err != nil {
					t.Fatal(err)
				}
			}
			terminal, _, err := mapper.add(messageStopEvent(types.StopReasonEndTurn))
			if err != nil {
				t.Fatal(err)
			}
			terminal, err = mapper.complete(terminal)
			if err != nil {
				t.Fatal(err)
			}
			if addErr := accumulator.Add(terminal); addErr != nil {
				t.Fatal(addErr)
			}
			response, err := accumulator.Response()
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := jsonv2.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var restored corechat.Response
			if unmarshalErr := jsonv2.Unmarshal(encoded, &restored); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			_, messages, err := mapProtocolMessages([]corechat.Message{*restored.Output.Message})
			if err != nil {
				t.Fatal(err)
			}
			blocks := messages[0].Content
			if len(blocks) != 2 {
				t.Fatalf("replayed %d reasoning blocks, want 2: %s", len(blocks), encoded)
			}
			for index, text := range []string{"first", "second"} {
				reasoning := blocks[index].(*types.ContentBlockMemberReasoningContent).Value
				if redacted {
					if got := string(reasoning.(*types.ReasoningContentBlockMemberRedactedContent).Value); got != "opaque-"+text {
						t.Fatalf("redacted block %d = %q", index, got)
					}
					continue
				}
				value := reasoning.(*types.ReasoningContentBlockMemberReasoningText).Value
				if aws.ToString(value.Text) != text || aws.ToString(value.Signature) != "sig-"+text {
					t.Fatalf("signed block %d = %#v", index, value)
				}
			}
		})
	}
}

func TestUnsignedReasoningRemainsVisibleAndDoesNotBlockContinuation(t *testing.T) {
	mapper := newStartedProtocolChunkAccumulator(t, "us.deepseek.r1-v1:0")
	delta, include, err := mapper.add(&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
		ContentBlockIndex: aws.Int32(0),
		Delta: &types.ContentBlockDeltaMemberReasoningContent{Value: &types.ReasoningContentBlockDeltaMemberText{
			Value: "consider the result",
		}},
	}})
	if err != nil || !include {
		t.Fatalf("reasoning delta = %v, %t", err, include)
	}
	var accumulator corechat.ResponseAccumulator
	if addErr := accumulator.Add(delta); addErr != nil {
		t.Fatal(addErr)
	}
	if addErr := accumulator.Add(&corechat.ResponseDelta{
		Parts: []corechat.PartDelta{corechat.NewTextDelta("answer")}, FinishReason: corechat.FinishReasonStop,
	}); addErr != nil {
		t.Fatal(addErr)
	}
	response, err := accumulator.Response()
	if err != nil {
		t.Fatal(err)
	}
	parts := response.Output.Message.Parts
	if len(parts) != 2 || parts[0].Kind != corechat.PartReasoning || parts[0].Text != "consider the result" || len(parts[0].ReasoningState) != 0 {
		t.Fatalf("unsigned reasoning output = %#v", parts)
	}
	_, messages, err := mapProtocolMessages([]corechat.Message{*response.Output.Message})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || len(messages[0].Content) != 1 {
		t.Fatalf("continuation messages = %#v", messages)
	}
	text, ok := messages[0].Content[0].(*types.ContentBlockMemberText)
	if !ok || text.Value != "answer" {
		t.Fatalf("continuation content = %#v", messages[0].Content)
	}
}
