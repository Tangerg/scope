package main

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"

	corecontent "github.com/Tangerg/scope/core/content"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

const (
	minimumDuplicateBytes   = 1024
	unchangedContextPolicy  = "unchanged"
	duplicateOutputPolicyID = "duplicate_tool_output"
)

type contextPolicy struct {
	ID      string
	Reducer interaction.ContextProcessor
}

type duplicateOutputReference struct {
	ToolCallID string `json:"identical_output_of_tool_call"`
}

// This policy keeps every call/result pair and every distinct output. It only
// factors repeated successful, unannotated plain text into backward references;
// it neither summarizes evidence nor treats an output as permission to act.
type duplicateToolOutputReducer struct{}

func (duplicateToolOutputReducer) PrepareContext(ctx context.Context, _ interaction.ContextInvocation, request *chat.Request) ([]chat.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	resultCounts := countToolResults(request.Messages)
	type outputKey struct{ name, text string }
	first := make(map[outputKey]string)
	messages := request.Clone().Messages
	for _, message := range messages {
		for _, part := range message.Parts {
			text, eligible := duplicateCandidate(message, part, resultCounts)
			if !eligible {
				continue
			}
			key := outputKey{part.ToolResult.Name, text}
			earlier, exists := first[key]
			if !exists {
				first[key] = part.ToolResult.ID
				continue
			}
			if err := referenceEarlierOutput(part.ToolResult, earlier); err != nil {
				return nil, err
			}
		}
	}
	return messages, nil
}

func countToolResults(messages []chat.Message) map[string]int {
	counts := make(map[string]int)
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Kind == chat.PartToolResult {
				counts[part.ToolResult.ID]++
			}
		}
	}
	return counts
}

// duplicateCandidate admits only a uniquely identified, successful,
// unannotated plain-text output large enough to be worth a reference.
func duplicateCandidate(message chat.Message, part chat.Part, resultCounts map[string]int) (string, bool) {
	if part.Kind != chat.PartToolResult || len(message.Metadata) != 0 || len(part.Metadata) != 0 {
		return "", false
	}
	result := part.ToolResult
	if result.IsError || resultCounts[result.ID] != 1 || len(result.Output.Details) != 0 || len(result.Output.Content) != 1 {
		return "", false
	}
	block := result.Output.Content[0]
	if block.Kind != corecontent.Text || len(block.Citations) != 0 || len(block.Metadata) != 0 || len(block.Text) < minimumDuplicateBytes {
		return "", false
	}
	return block.Text, true
}

// referenceEarlierOutput replaces result's output with a reference to the
// earlier identical one only when the reference is smaller.
func referenceEarlierOutput(result *chat.ToolResult, earlier string) error {
	reference, err := jsonv2.Marshal(duplicateOutputReference{ToolCallID: earlier})
	if err != nil {
		return fmt.Errorf("encode duplicate output reference: %w", err)
	}
	// References stay below the eligibility floor. They cannot become
	// another full-output candidate on later calls or after restoration.
	if len(reference) >= minimumDuplicateBytes {
		return nil
	}
	replacement := toolcontract.NewTextOutput(string(reference))
	originalBytes, err := jsonv2.Marshal(result.Output)
	if err != nil {
		return fmt.Errorf("measure original Tool output: %w", err)
	}
	replacementBytes, err := jsonv2.Marshal(replacement)
	if err != nil {
		return fmt.Errorf("measure replacement Tool output: %w", err)
	}
	if len(replacementBytes) < len(originalBytes) {
		result.Output = replacement
	}
	return nil
}

var _ interaction.ContextProcessor = duplicateToolOutputReducer{}
