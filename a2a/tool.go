package a2a

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	corechat "github.com/Tangerg/scope/core/chat"
	corejsonschema "github.com/Tangerg/scope/core/jsonschema"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

var errEmptyToolName = errors.New("a2a: tool name must not be empty")

// inputFailure reports arguments rejected before the remote agent is called as a
// definite, model-visible Tool failure rather than an uncertain error.
func inputFailure(cause error) error {
	failure, err := toolcontract.NewFailure(toolcontract.FailureConfig{
		Kind:   toolcontract.FailureKindFailed,
		Output: corechat.NewTextToolOutput(cause.Error()),
		Cause:  cause,
	})
	if err != nil {
		return errors.Join(cause, err)
	}
	return failure
}

type callArguments struct {
	Message string `json:"message" jsonschema_description:"The natural-language request to send to the remote agent."`
}

// remoteTool is immutable after construction and does not own its client.
type remoteTool struct {
	client            *a2aclient.Client
	definition        corechat.ToolDefinition
	concurrencyPolicy ToolConcurrencyPolicy
}

var _ toolcontract.Tool = remoteTool{}

type remoteToolConfig struct {
	client            *a2aclient.Client
	card              *sdka2a.AgentCard
	name              string
	concurrencyPolicy ToolConcurrencyPolicy
}

func newRemoteTool(config remoteToolConfig) (remoteTool, error) {
	if config.name == "" {
		config.name = corechat.SanitizeToolName(config.card.Name)
	}
	if config.name == "" {
		return remoteTool{}, errEmptyToolName
	}
	inputSchema, err := corejsonschema.For[callArguments]()
	if err != nil {
		return remoteTool{}, fmt.Errorf("a2a: derive input schema: %w", err)
	}
	definition := corechat.ToolDefinition{
		Name:        config.name,
		Description: describeAgent(config.card),
		InputSchema: inputSchema.JSON(),
	}
	if err := definition.Validate(); err != nil {
		return remoteTool{}, fmt.Errorf("a2a: build tool for agent %q: %w", config.card.Name, err)
	}
	return remoteTool{
		client:            config.client,
		definition:        definition,
		concurrencyPolicy: config.concurrencyPolicy,
	}, nil
}

func (r remoteTool) Definition() corechat.ToolDefinition { return r.definition.Clone() }

func (r remoteTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	return r.concurrencyPolicy
}

func (r remoteTool) Call(ctx context.Context, invocation toolcontract.Invocation) (out toolcontract.Result, err error) {
	ctx, span := a2aTracer.Start(ctx, "a2a.agent.call "+r.definition.Name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String(attrAgentName, r.definition.Name)),
	)
	defer func() {
		if err != nil {
			recordSpanError(span, err)
		}
		span.End()
	}()

	input, err := parseCallArguments(invocation.Arguments())
	if err != nil {
		return toolcontract.Result{}, inputFailure(fmt.Errorf("a2a: decode arguments for agent %q: %w", r.definition.Name, err))
	}

	projection := textProjection{}
	req := &sdka2a.SendMessageRequest{Message: projection.userMessage(input.Message)}
	result, err := r.client.SendMessage(ctx, req)
	if err != nil {
		return toolcontract.Result{}, fmt.Errorf("a2a: call agent %q: %w", r.definition.Name, err)
	}

	text, err := projection.result(result)
	if err != nil {
		return toolcontract.Result{}, fmt.Errorf("a2a: result from agent %q: %w", r.definition.Name, err)
	}
	return toolcontract.Result{Output: corechat.NewTextToolOutput(text)}, nil
}

func parseCallArguments(arguments []byte) (callArguments, error) {
	var input callArguments
	if err := jsonv2.Unmarshal(arguments, &input, jsonv2.RejectUnknownMembers(true)); err != nil {
		return callArguments{}, err
	}
	if strings.TrimSpace(input.Message) == "" {
		return callArguments{}, errors.New("message must not be empty")
	}
	return input, nil
}

func describeAgent(card *sdka2a.AgentCard) string {
	var b strings.Builder
	b.WriteString(card.Description)
	if len(card.Skills) > 0 {
		names := make([]string, 0, len(card.Skills))
		for _, skill := range card.Skills {
			names = append(names, skill.Name)
		}
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString("Skills: ")
		b.WriteString(strings.Join(names, ", "))
		b.WriteString(".")
	}
	return b.String()
}
