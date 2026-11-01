package main

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/Tangerg/scope/agent"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func newDeployment(remote *remoteLedger, model *receiptModel) (agentrun.Program[agentrun.Payload, agentrun.Payload], agent.ToolSet, error) {
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "record_note", Description: "Record one note remotely."},
		func(ctx context.Context, input note) (string, error) {
			invocation, ok := agent.ToolInvocationFromContext(ctx)
			if !ok {
				return "", errors.New("example: missing Engine attribution")
			}
			if submitErr := remote.submit(ctx, invocation.EffectID(), input); submitErr != nil {
				return "", submitErr
			}
			return "", errors.New("remote: response lost after recording the outcome")
		})
	if err != nil {
		return agentrun.Program[agentrun.Payload, agentrun.Payload]{}, agent.ToolSet{}, err
	}
	tools, err := agent.NewToolSet(agent.ToolSetConfig{
		Name: "example.receipt_tools", Description: "A remote write whose response is lost.", Tools: []tool.Tool{executable},
		ImplementationDigest: agentrun.ComputeDigest([]byte("receipt-tool")),
		ConfigurationDigest:  agentrun.ComputeDigest([]byte("record_note")),
	})
	if err != nil {
		return agentrun.Program[agentrun.Payload, agentrun.Payload]{}, tools, err
	}
	definition, err := agent.NewDefinition(agent.DefinitionConfig{
		Name: "example.reconciliation", Description: "Report a reconciled business receipt.",
		MaxModelCalls: agentrun.NewQuota(2), Tools: tools,
	})
	if err != nil {
		return agentrun.Program[agentrun.Payload, agentrun.Payload]{}, tools, err
	}
	dispatcher, err := agent.NewDispatcher(definition, agent.DispatcherConfig{Model: model})
	if err != nil {
		return agentrun.Program[agentrun.Payload, agentrun.Payload]{}, tools, err
	}
	deployment, err := agentrun.Bind[agentrun.Payload, agentrun.Payload](agentrun.BindingConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agentrun.ComputeDigest([]byte("receipt-interaction")),
		ConfigurationDigest:  agentrun.ComputeDigest([]byte("record one release note; report its receipt")),
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
