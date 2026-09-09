package bedrock

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

func TestChatBuildConverseInput(t *testing.T) {
	temperature := 0.4
	image, err := media.NewBytes("image/png", []byte("png"))
	if err != nil {
		t.Fatal(err)
	}
	reasoning, err := NewReasoningPart("thinking", []byte("sig"))
	if err != nil {
		t.Fatal(err)
	}
	request := &corechat.Request{
		Messages: []corechat.Message{
			corechat.NewSystemMessage("system"),
			corechat.NewUserMessage(corechat.NewTextPart("look"), corechat.NewMediaPart(image)),
			corechat.NewAssistantMessage(
				reasoning,
				corechat.NewToolCallPart(corechat.ToolCall{ID: "call-1", Name: "weather", Arguments: `{"city":"Paris"}`}),
			),
			corechat.NewToolMessage(corechat.ToolResult{
				ID: "call-1", Name: "weather", Output: corechat.NewTextToolOutput("rain"), IsError: true,
			}),
		},
		Tools: []corechat.ToolDefinition{{
			Name: "weather", Description: "Get weather", InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		Options: corechat.Options{Temperature: &temperature},
	}
	if setExtensionErr := request.Options.Extensions.Set(ChatRequestExtensionKey, ChatRequestOptions{
		AdditionalModelRequestFields: map[string]any{"thinking": true},
		RequestMetadata:              map[string]string{"tenant": "test"},
	}); setExtensionErr != nil {
		t.Fatal(setExtensionErr)
	}

	model := &Chat{api: &api{}, defaults: corechat.Options{Model: "anthropic.claude-test"}}
	input, modelName, err := model.buildConverseInput(request)
	if err != nil {
		t.Fatal(err)
	}
	if modelName != "anthropic.claude-test" || aws.ToString(input.ModelId) != modelName {
		t.Fatalf("model = %q / %q", modelName, aws.ToString(input.ModelId))
	}
	if len(input.System) != 1 || len(input.Messages) != 3 {
		t.Fatalf("system/messages = %d/%d", len(input.System), len(input.Messages))
	}
	if input.InferenceConfig == nil || input.InferenceConfig.Temperature == nil || *input.InferenceConfig.Temperature != 0.4 {
		t.Fatalf("inference = %#v", input.InferenceConfig)
	}
	if input.ToolConfig == nil || len(input.ToolConfig.Tools) != 1 {
		t.Fatalf("tools = %#v", input.ToolConfig)
	}
	if input.RequestMetadata["tenant"] != "test" || input.AdditionalModelRequestFields == nil {
		t.Fatalf("native options = %#v", input)
	}
	toolResult, ok := input.Messages[2].Content[0].(*types.ContentBlockMemberToolResult)
	if !ok || toolResult.Value.Status != types.ToolResultStatusError {
		t.Fatalf("tool result = %#v", input.Messages[2].Content[0])
	}
}

func TestMapProtocolConverseResponse(t *testing.T) {
	output := &bedrockruntime.ConverseOutput{
		Output: &types.ConverseOutputMemberMessage{Value: types.Message{
			Role: types.ConversationRoleAssistant,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberReasoningContent{Value: &types.ReasoningContentBlockMemberReasoningText{Value: types.ReasoningTextBlock{Text: aws.String("think"), Signature: aws.String("sig")}}},
				&types.ContentBlockMemberReasoningContent{Value: &types.ReasoningContentBlockMemberRedactedContent{Value: []byte("opaque")}},
				&types.ContentBlockMemberText{Value: "answer"},
				&types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{ToolUseId: aws.String("call-1"), Name: aws.String("weather"), Input: toBedrockDocument(map[string]any{"city": "Paris"})}},
			},
		}},
		StopReason: types.StopReasonToolUse,
		Usage: &types.TokenUsage{
			InputTokens: aws.Int32(11), OutputTokens: aws.Int32(7), CacheReadInputTokens: aws.Int32(3),
		},
	}

	response, err := mapProtocolConverseResponse("model", output)
	if err != nil {
		t.Fatal(err)
	}
	if response.Metadata.Model != "model" || response.Output.FinishReason != corechat.FinishReasonToolCalls {
		t.Fatalf("response = %#v", response)
	}
	wantKinds := []corechat.PartKind{corechat.PartReasoning, corechat.PartReasoning, corechat.PartText, corechat.PartToolCall}
	parts := response.Output.Message.Parts
	for index, want := range wantKinds {
		if parts[index].Kind != want {
			t.Fatalf("part[%d] = %q, want %q", index, parts[index].Kind, want)
		}
	}
	kind, found, err := ReasoningBlockKindOf(parts[1])
	if err != nil || !found || kind != ReasoningBlockRedacted || string(parts[1].ReasoningState) != "opaque" {
		t.Fatalf("redacted reasoning = %#v/%q/%v/%v", parts[1], kind, found, err)
	}
	// Converse reports inputTokens as the non-cached part only, so the Core
	// total is 11 + 3.
	usage := response.Metadata.Usage
	if usage.InputTokens != 14 || usage.OutputTokens != 7 || usage.CacheReadInputTokens == nil || *usage.CacheReadInputTokens != 3 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestProtocolChunkAccumulatorRetainsToolIdentity(t *testing.T) {
	accumulator := newProtocolChunkAccumulator("model")
	index := int32(2)
	start := &types.ConverseStreamOutputMemberContentBlockStart{Value: types.ContentBlockStartEvent{
		ContentBlockIndex: &index,
		Start: &types.ContentBlockStartMemberToolUse{Value: types.ToolUseBlockStart{
			ToolUseId: aws.String("call-1"), Name: aws.String("weather"),
		}},
	}}
	response, include, err := accumulator.add(start)
	if err != nil || !include || response.Parts[0].ToolCall.Name != "weather" {
		t.Fatalf("start = %#v, %v, %v", response, include, err)
	}

	arguments := `{"city":"Paris"}`
	delta := &types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
		ContentBlockIndex: &index,
		Delta:             &types.ContentBlockDeltaMemberToolUse{Value: types.ToolUseBlockDelta{Input: &arguments}},
	}}
	response, include, err = accumulator.add(delta)
	if err != nil || !include {
		t.Fatalf("delta = %#v, %v, %v", response, include, err)
	}
	call := response.Parts[0].ToolCall
	if call.ID != "call-1" || call.Name != "weather" || call.Arguments != arguments {
		t.Fatalf("tool call = %#v", call)
	}
}

// A toolUse block start is the only place Converse names a tool call, so one
// that opens without an index, id, or name leaves nothing to attach its input
// deltas to. Ignoring it used to be indistinguishable from ignoring the start
// of a text block: with input deltas the stream then failed on an "unknown
// content block", and with none the call disappeared entirely.
func TestProtocolChunkAccumulatorRejectsAnUnidentifiedToolBlockStart(t *testing.T) {
	index := int32(0)
	tests := map[string]types.ContentBlockStartEvent{
		"no index": {
			Start: &types.ContentBlockStartMemberToolUse{Value: types.ToolUseBlockStart{
				ToolUseId: aws.String("call-1"), Name: aws.String("weather"),
			}},
		},
		"no id": {
			ContentBlockIndex: &index,
			Start: &types.ContentBlockStartMemberToolUse{Value: types.ToolUseBlockStart{
				Name: aws.String("weather"),
			}},
		},
		"no name": {
			ContentBlockIndex: &index,
			Start: &types.ContentBlockStartMemberToolUse{Value: types.ToolUseBlockStart{
				ToolUseId: aws.String("call-1"),
			}},
		},
	}

	for name, event := range tests {
		t.Run(name, func(t *testing.T) {
			accumulator := newProtocolChunkAccumulator("model")
			_, _, err := accumulator.add(&types.ConverseStreamOutputMemberContentBlockStart{Value: event})
			if err == nil {
				t.Fatal("add() = nil error, want the unidentified toolUse block reported")
			}
		})
	}
}

// A start payload that is not toolUse still opens an ordinary content block,
// so it is passed over rather than reported.
func TestProtocolChunkAccumulatorIgnoresANonToolBlockStart(t *testing.T) {
	accumulator := newProtocolChunkAccumulator("model")
	index := int32(0)
	delta, include, err := accumulator.add(&types.ConverseStreamOutputMemberContentBlockStart{
		Value: types.ContentBlockStartEvent{ContentBlockIndex: &index},
	})
	if err != nil || include || delta != nil {
		t.Fatalf("add() = %#v, %v, %v; want it passed over", delta, include, err)
	}
}

func TestProtocolChunkAccumulatorPreservesReasoningKind(t *testing.T) {
	accumulator := newProtocolChunkAccumulator("model")
	index := int32(0)
	event := &types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
		ContentBlockIndex: &index,
		Delta: &types.ContentBlockDeltaMemberReasoningContent{Value: &types.ReasoningContentBlockDeltaMemberText{
			Value: "thinking",
		}},
	}}
	delta, include, err := accumulator.add(event)
	if err != nil || !include || len(delta.Parts) != 1 {
		t.Fatalf("reasoning delta = %#v, %v, %v", delta, include, err)
	}
	part := delta.Parts[0]
	kind, found, err := part.Metadata.Decode[string](chatReasoningKindKey)
	if err != nil || !found || kind != chatReasoningText || part.Text != "thinking" {
		t.Fatalf("reasoning part = %#v, %q, %v, %v", part, kind, found, err)
	}
}

func TestMapProtocolToolsPreservesPortableChoice(t *testing.T) {
	definitions := []corechat.ToolDefinition{{Name: "weather", InputSchema: json.RawMessage(`{"type":"object"}`)}}

	configuration, err := mapProtocolTools(definitions, &corechat.ToolChoice{Mode: corechat.ToolChoiceRequired})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := configuration.ToolChoice.(*types.ToolChoiceMemberAny); !ok {
		t.Fatalf("required choice = %#v", configuration.ToolChoice)
	}

	configuration, err = mapProtocolTools(definitions, &corechat.ToolChoice{Mode: corechat.ToolChoiceNamed, Name: "weather"})
	if err != nil {
		t.Fatal(err)
	}
	named, ok := configuration.ToolChoice.(*types.ToolChoiceMemberTool)
	if !ok || aws.ToString(named.Value.Name) != "weather" {
		t.Fatalf("named choice = %#v", configuration.ToolChoice)
	}

	configuration, err = mapProtocolTools(definitions, &corechat.ToolChoice{Mode: corechat.ToolChoiceNone})
	if err != nil || configuration != nil {
		t.Fatalf("none choice = %#v, %v", configuration, err)
	}

	_, err = mapProtocolTools(definitions, &corechat.ToolChoice{
		Mode: corechat.ToolChoiceAuto, Parallelism: corechat.ToolParallelismSingle,
	})
	if err == nil {
		t.Fatal("single parallelism was silently ignored")
	}
}

func TestMediaToBlockSupportsOfficialConverseModalities(t *testing.T) {
	document, err := media.NewBytes("application/pdf", []byte("pdf"))
	if err != nil {
		t.Fatal(err)
	}
	document.Name = "design.pdf"
	block, err := mediaToBlock(document)
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	documentBlock, ok := block.(*types.ContentBlockMemberDocument)
	if !ok || documentBlock.Value.Format != types.DocumentFormatPdf || aws.ToString(documentBlock.Value.Name) != "design" {
		t.Fatalf("document block = %#v", block)
	}

	audio, err := media.NewURI("audio/wav", "s3://bucket/input.wav")
	if err != nil {
		t.Fatal(err)
	}
	block, err = mediaToBlock(audio)
	if err != nil {
		t.Fatalf("audio: %v", err)
	}
	audioBlock, ok := block.(*types.ContentBlockMemberAudio)
	if !ok || audioBlock.Value.Format != types.AudioFormatWav {
		t.Fatalf("audio block = %#v", block)
	}
	source, ok := audioBlock.Value.Source.(*types.AudioSourceMemberS3Location)
	if !ok || aws.ToString(source.Value.Uri) != "s3://bucket/input.wav" {
		t.Fatalf("audio source = %#v", audioBlock.Value.Source)
	}
}

// Converse's MaxTokens is an int32 while Core carries an int64 it only checks
// for being positive. A bare conversion wraps a larger value to a negative
// token limit and sends it, so the bound is checked before narrowing — the same
// guard the google adapter already applies to the same field.
func TestInferenceOptionsRejectMaxTokensBeyondInt32(t *testing.T) {
	t.Parallel()

	beyond := int64(math.MaxInt32) + 1
	_, err := mapInferenceOptions(corechat.Options{MaxOutputTokens: &beyond})
	if err == nil {
		t.Fatal("mapInferenceOptions() = nil error, want an int32 bound error")
	}
	if !strings.Contains(err.Error(), "exceeds the int32 Converse accepts") {
		t.Fatalf("mapInferenceOptions() = %v, want an int32 bound error", err)
	}

	within := int64(math.MaxInt32)
	configuration, err := mapInferenceOptions(corechat.Options{MaxOutputTokens: &within})
	if err != nil {
		t.Fatalf("mapInferenceOptions(MaxInt32) = %v, want nil", err)
	}
	if configuration.MaxTokens == nil || *configuration.MaxTokens != math.MaxInt32 {
		t.Fatalf("MaxTokens = %v, want %d", configuration.MaxTokens, int32(math.MaxInt32))
	}
}
