package bedrock

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	corechat "github.com/Tangerg/scope/core/chat"
)

func textDeltaEvent(text string) types.ConverseStreamOutput {
	return &types.ConverseStreamOutputMemberContentBlockDelta{
		Value: types.ContentBlockDeltaEvent{
			Delta: &types.ContentBlockDeltaMemberText{Value: text},
		},
	}
}

func messageStopEvent(reason types.StopReason) types.ConverseStreamOutput {
	return &types.ConverseStreamOutputMemberMessageStop{
		Value: types.MessageStopEvent{StopReason: reason},
	}
}

// An event stream can end cleanly in the middle of a message, which stream.Err
// does not report. Only a messageStop event says the message is whole, so the
// accumulator tracks whether one arrived.
func TestChunkAccumulatorTracksTerminalEvent(t *testing.T) {
	t.Parallel()

	accumulator := newProtocolChunkAccumulator("model")
	if _, _, err := accumulator.add(textDeltaEvent("half")); err != nil {
		t.Fatalf("add(text) = %v, want nil", err)
	}
	if accumulator.terminated() {
		t.Fatal("terminated() = true after a content delta, want false")
	}

	response, include, err := accumulator.add(messageStopEvent(types.StopReasonEndTurn))
	if err != nil || !include {
		t.Fatalf("add(messageStop) = (%v, %t), want (nil, true)", err, include)
	}
	if response.FinishReason != corechat.FinishReasonStop {
		t.Fatalf("FinishReason = %q, want %q", response.FinishReason, corechat.FinishReasonStop)
	}
	if !accumulator.terminated() {
		t.Fatal("terminated() = false after messageStop, want true")
	}
}

// Two messageStop events would leave the finish reason ambiguous.
func TestChunkAccumulatorRejectsSecondTerminalEvent(t *testing.T) {
	t.Parallel()

	accumulator := newProtocolChunkAccumulator("model")
	if _, _, err := accumulator.add(messageStopEvent(types.StopReasonEndTurn)); err != nil {
		t.Fatalf("add(messageStop) = %v, want nil", err)
	}
	_, _, err := accumulator.add(messageStopEvent(types.StopReasonMaxTokens))
	if err == nil || !strings.Contains(err.Error(), "more than one messageStop") {
		t.Fatalf("add(second messageStop) = %v, want a duplicate-terminal error", err)
	}
}
