package main

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
)

const (
	minimumDuplicateBytes   = 1024
	unchangedContextPolicy  = "unchanged"
	duplicateOutputPolicyID = "duplicate_tool_output"
)

type contextPolicy struct {
	ID      string
	Reducer interaction.ModelContextReducer
}

type duplicateOutputReference struct {
	ToolCallID string `json:"identical_output_of_tool_call"`
}

// This policy keeps every call/result pair and every distinct output. It only
// factors repeated successful, unannotated plain text into backward references;
// it neither summarizes evidence nor treats an output as permission to act.
type duplicateToolOutputReducer struct{}

func (duplicateToolOutputReducer) ReduceModelContext(ctx context.Context, _ interaction.ModelInvocation, request *chat.Request) ([]chat.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	ids := make(map[string]int)
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			if part.Kind == chat.PartToolResult {
				ids[part.ToolResult.ID]++
			}
		}
	}
	type outputKey struct{ name, text string }
	first := make(map[outputKey]string)
	messages := request.Clone().Messages
	for _, message := range messages {
		for _, part := range message.Parts {
			result := part.ToolResult
			if part.Kind != chat.PartToolResult || result.IsError || ids[result.ID] != 1 || len(message.Metadata) != 0 || len(part.Metadata) != 0 {
				continue
			}
			content := result.Output.Content
			if len(result.Output.Details) != 0 || len(content) != 1 || content[0].Kind != chat.PartText || len(content[0].Citations) != 0 || len(content[0].Metadata) != 0 || len(content[0].Text) < minimumDuplicateBytes {
				continue
			}
			key := outputKey{result.Name, content[0].Text}
			id, exists := first[key]
			if !exists {
				first[key] = result.ID
				continue
			}
			reference, err := jsonv2.Marshal(duplicateOutputReference{ToolCallID: id})
			if err != nil {
				return nil, fmt.Errorf("encode duplicate output reference: %w", err)
			}
			// References stay below the eligibility floor. They cannot become
			// another full-output candidate on later calls or after restoration.
			if len(reference) >= minimumDuplicateBytes {
				continue
			}
			replacement := chat.NewTextToolOutput(string(reference))
			originalBytes, err := jsonv2.Marshal(result.Output)
			if err != nil {
				return nil, fmt.Errorf("measure original Tool output: %w", err)
			}
			replacementBytes, err := jsonv2.Marshal(replacement)
			if err != nil {
				return nil, fmt.Errorf("measure replacement Tool output: %w", err)
			}
			if len(replacementBytes) < len(originalBytes) {
				result.Output = replacement
			}
		}
	}
	return messages, nil
}

var _ interaction.ModelContextReducer = duplicateToolOutputReducer{}
