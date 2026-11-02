package main

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/Tangerg/scope/agent/interaction"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

const (
	recordNoteToolName    = "record_note"
	receiptModelCallLimit = 2
)

func newProgram(compiler *agentrun.Compiler, remote *remoteLedger, model *receiptModel) (agentrun.Program[interaction.Input, interaction.Output], error) {
	executable, err := tool.NewFunc(tool.FuncConfig{Name: recordNoteToolName, Description: "Record one note remotely."},
		func(ctx context.Context, input note) (string, error) {
			invocation, ok := interaction.ToolInvocationFromContext(ctx)
			if !ok {
				return "", errors.New("example: missing effect attribution")
			}
			if submitErr := remote.submit(ctx, invocation.EffectID(), input); submitErr != nil {
				return "", submitErr
			}
			return "", errors.New("remote: response lost after recording the outcome")
		})
	if err != nil {
		return agentrun.Program[interaction.Input, interaction.Output]{}, err
	}
	tools, err := interaction.NewToolSet(interaction.ToolSetConfig{
		Revision: "receipt-tool.v1", Tools: []tool.Tool{executable},
	})
	if err != nil {
		return agentrun.Program[interaction.Input, interaction.Output]{}, err
	}
	return interaction.Compile(compiler, interaction.Config{
		Name: "example.reconciliation", Description: "Report a reconciled business receipt.",
		Revision: "receipt-interaction.v1", Model: model,
		MaxModelCalls: agentrun.NewQuota(receiptModelCallLimit), Tools: tools,
	})
}

type receiptModel struct{ calls atomic.Int32 }

func (r *receiptModel) Call(ctx context.Context, request *chat.Request) (*chat.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	r.calls.Add(1)
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			if part.Kind != chat.PartToolResult {
				continue
			}
			text, ok := part.ToolResult.Output.Text()
			if !ok {
				return nil, errors.New("example: expected a text receipt")
			}
			if part.ToolResult.IsError {
				text = "rejected: " + text
			}
			return &chat.Response{Output: &chat.Output{
				Message: new(chat.NewAssistantMessage(chat.NewTextPart(text))), FinishReason: chat.FinishReasonStop,
			}}, nil
		}
	}
	return &chat.Response{Output: &chat.Output{
		Message: new(chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{
			ID: "call:record", Name: recordNoteToolName, Arguments: `{"text":"release note"}`,
		}))), FinishReason: chat.FinishReasonToolCalls,
	}}, nil
}
