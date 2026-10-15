package main

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func newDeployment(remote *remoteLedger, model *receiptModel) (agent.Deployment, interaction.ToolSet, error) {
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "record_note", Description: "Record one note remotely."},
		func(ctx context.Context, input note) (string, error) {
			invocation, ok := interaction.ToolInvocationFromContext(ctx)
			if !ok {
				return "", errors.New("example: missing Engine attribution")
			}
			if submitErr := remote.submit(ctx, invocation.EffectID(), input); submitErr != nil {
				return "", submitErr
			}
			return "", errors.New("remote: response lost after recording the outcome")
		})
	if err != nil {
		return agent.Deployment{}, interaction.ToolSet{}, err
	}
	tools, err := interaction.NewToolSet(interaction.ToolSetConfig{
		Name: "example.receipt_tools", Description: "A remote write whose response is lost.", Tools: []tool.Tool{executable},
		ImplementationDigest: agent.ComputeDigest([]byte("receipt-tool")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("record_note")),
	})
	if err != nil {
		return agent.Deployment{}, tools, err
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "example.reconciliation", Description: "Report a reconciled business receipt.",
		MaxModelCalls: agent.NewQuota(2), Tools: tools,
	})
	if err != nil {
		return agent.Deployment{}, tools, err
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{Model: model})
	if err != nil {
		return agent.Deployment{}, tools, err
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("receipt-interaction")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("record one release note; report its receipt")),
	})
	return deployment, tools, err
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
			ID: "call:record", Name: "record_note", Arguments: `{"text":"release note"}`,
		}))), FinishReason: chat.FinishReasonToolCalls,
	}}, nil
}
