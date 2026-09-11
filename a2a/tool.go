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

type callArguments struct {
	Message string `json:"message" jsonschema_description:"The natural-language request to send to the remote agent."`
}

// remoteTool wraps a remote A2A agent as a [tool.Tool]. Each Call sends the
// argument text as an A2A message and returns the agent's reply, so an
// agent can delegate to a remote agent through the ordinary tool-calling
// loop. A task that does not complete successfully is mapped to
// [*RemoteAgentError] (use [errors.AsType]) so a remote failure or unsupported
// continuation is not fed back as a successful result.
//
// The wrapper is immutable after construction and does not own the client.
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
		config.name = sanitizeToolName(config.card.Name)
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

// ConcurrencyPolicy exports only the host's independent scheduling declaration.
func (r remoteTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	return r.concurrencyPolicy
}

// Each remote call owns a client span named `a2a.agent.call <name>` with
// gen_ai.agent.name attribution; remote failure records the error and marks
// the span failed rather than hiding transport state in the textual result.
func (r remoteTool) Call(ctx context.Context, invocation toolcontract.Invocation) (out corechat.ToolOutput, err error) {
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
		return corechat.ToolOutput{}, fmt.Errorf("a2a: decode arguments for agent %q: %w", r.definition.Name, err)
	}

	projection := textProjection{}
	req := &sdka2a.SendMessageRequest{Message: projection.userMessage(input.Message)}
	result, err := r.client.SendMessage(ctx, req)
	if err != nil {
		return corechat.ToolOutput{}, fmt.Errorf("a2a: call agent %q: %w", r.definition.Name, err)
	}

	text, err := projection.result(result)
	if err != nil {
		return corechat.ToolOutput{}, fmt.Errorf("a2a: decode result from agent %q: %w", r.definition.Name, err)
	}
	return corechat.NewTextToolOutput(text), nil
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

// describeAgent builds the tool description from the card: its description
// plus a compact list of skill names so the model knows what the remote can
// do.
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

// sanitizeToolName maps an AgentCard name (which may contain spaces or
// punctuation) to a tool identifier: lowercased, with runs of non-alphanumeric
// characters collapsed to single underscores.
func sanitizeToolName(name string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}
