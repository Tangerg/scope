package a2a

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	corechat "github.com/Tangerg/scope/core/chat"
	corejsonschema "github.com/Tangerg/scope/core/jsonschema"
	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/otel/errortelemetry"
)

var errEmptyToolName = errors.New("a2a: tool name must not be empty")

// inputFailure reports arguments rejected before the remote agent is called as a
// definite, model-visible Tool failure rather than an uncertain error.
func inputFailure(cause error) error {
	failure, err := toolcontract.NewFailure(toolcontract.FailureConfig{
		Kind:   toolcontract.FailureKindFailed,
		Output: toolcontract.NewTextOutput(cause.Error()),
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

func (c callArguments) validate() error {
	if strings.TrimSpace(c.Message) == "" {
		return errors.New("a2a: message must not be empty")
	}
	return nil
}

// remoteTool is immutable after construction and does not own its client.
type remoteTool struct {
	binding           string
	client            *a2aclient.Client
	definition        toolcontract.Definition
	concurrencyPolicy ToolConcurrencyPolicy
}

var _ toolcontract.Tool = remoteTool{}

type remoteToolConfig struct {
	client            *a2aclient.Client
	card              *sdka2a.AgentCard
	name              string
	concurrencyPolicy ToolConcurrencyPolicy
	effect            toolcontract.Effect
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
	definition := toolcontract.Definition{
		Name:        config.name,
		Description: describeAgent(config.card),
		InputSchema: inputSchema.JSON(),
		Effect:      config.effect,
	}
	if err := definition.Validate(); err != nil {
		return remoteTool{}, fmt.Errorf("a2a: build tool for agent %q: %w", config.card.Name, err)
	}
	return remoteTool{
		client:            config.client,
		binding:           rand.Text(),
		definition:        definition,
		concurrencyPolicy: config.concurrencyPolicy,
	}, nil
}

func (r remoteTool) Definition() toolcontract.Definition { return r.definition.Clone() }

func (remoteTool) InputValidator() func([]byte) error {
	return func(arguments []byte) error {
		_, err := parseCallArguments(arguments)
		if err == nil {
			return nil
		}
		rejection, buildErr := toolcontract.NewInputRejection(toolcontract.InputRejectionConfig{
			Message: "Provide a JSON object with a non-empty message string and no other fields.",
			Cause:   err,
		})
		if buildErr != nil {
			return buildErr
		}
		return rejection
	}
}

func (r remoteTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	return r.concurrencyPolicy
}

func (r remoteTool) Call(ctx context.Context, invocation toolcontract.Invocation) (out toolcontract.Result, err error) {
	ctx, span := a2aTracer.Start(ctx, "a2a.agent.call "+r.definition.Name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.GenAIAgentName(r.definition.Name)),
	)
	defer func() {
		if err != nil {
			errortelemetry.Record(span, errortelemetry.Classify(err))
		}
		span.End()
	}()

	input, err := parseCallArguments(invocation.Arguments())
	if err != nil {
		return toolcontract.Result{}, inputFailure(fmt.Errorf("a2a: decode arguments for agent %q: %w", r.definition.Name, err))
	}

	projection := textProjection{}
	req := &sdka2a.SendMessageRequest{Message: projection.userMessage(input.Message)}
	if key := invocation.IdempotencyKey(); key != "" {
		// A trusted server can reconcile the original identity. The host's Effect
		// declaration, not this transport identity, establishes safe replay.
		req.Message.ID = string(key)
	}
	if c, ok := invocation.Continuation(); ok {
		req.Message, err = r.reply(invocation, c)
		if err != nil {
			return toolcontract.Result{}, err
		}
	}
	result, err := r.client.SendMessage(ctx, req)
	if err != nil {
		return toolcontract.Result{}, fmt.Errorf("a2a: call agent %q: %w", r.definition.Name, err)
	}

	if task, ok := result.(*sdka2a.Task); ok && task != nil {
		if req.Message.TaskID != "" && (task.ID != req.Message.TaskID || task.ContextID != req.Message.ContextID) {
			return toolcontract.Result{}, ErrContinuationBinding
		}
		if task.Status.State == sdka2a.TaskStateInputRequired {
			return r.awaitInput(task, invocation)
		}
	}
	text, err := projection.result(result)
	if err != nil {
		return toolcontract.Result{}, fmt.Errorf("a2a: result from agent %q: %w", r.definition.Name, err)
	}
	return toolcontract.Result{Output: toolcontract.NewTextOutput(text)}, nil
}

func parseCallArguments(arguments []byte) (callArguments, error) {
	var input callArguments
	if err := jsonv2.Unmarshal(arguments, &input, jsonv2.RejectUnknownMembers(true)); err != nil {
		return callArguments{}, err
	}
	if err := input.validate(); err != nil {
		return callArguments{}, err
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

var ErrContinuationBinding = errors.New("a2a: continuation does not match the original remote task")

type remoteTurn struct {
	Binding  string            `json:"binding"`
	Original [sha256.Size]byte `json:"original"`
	Task     sdka2a.TaskID     `json:"task"`
	Context  string            `json:"context"`
	Reply    string            `json:"reply"`
	Wait     WaitAddress       `json:"wait,omitzero"`
}

func (r remoteTool) awaitInput(task *sdka2a.Task, inv toolcontract.Invocation) (toolcontract.Result, error) {
	if task.ID == "" || task.ContextID == "" || task.Status.Message == nil {
		return toolcontract.Result{}, fmt.Errorf("%w: input-required task lacks a bound question", ErrInvalidResult)
	}
	var question json.RawMessage
	var schema corejsonschema.Schema
	var err error
	msg := task.Status.Message
	declared, hasWait := msg.Metadata[WaitMetadataKey]
	_, hasSchema := msg.Metadata[AnswerSchemaMetadataKey]
	var wait WaitAddress
	if hasWait != hasSchema {
		return toolcontract.Result{}, fmt.Errorf("%w: incomplete or malformed remote wait binding", ErrInvalidResult)
	}
	if hasWait {
		wait, err = ParseWaitAddress(declared)
		if err != nil {
			return toolcontract.Result{}, err
		}
		raw, ok := msg.Metadata[AnswerSchemaMetadataKey].(string)
		if !ok || len(msg.Parts) != 1 || msg.Parts[0] == nil {
			return toolcontract.Result{}, ErrInvalidResult
		}
		schema, err = corejsonschema.Parse([]byte(raw))
		if err != nil {
			return toolcontract.Result{}, err
		}
		p := msg.Parts[0]
		txt, ok := p.Content.(sdka2a.Text)
		if !ok || p.Filename != "" || len(p.Metadata) > 0 {
			return toolcontract.Result{}, ErrUnsupportedContent
		}
		switch p.MediaType {
		case "application/json":
			question = json.RawMessage(txt)
		case "", plainTextMediaType:
			question, err = jsonv2.Marshal(string(txt))
		default:
			return toolcontract.Result{}, ErrUnsupportedContent
		}
	} else {
		prompt, projectErr := (textProjection{}).parts(msg.Parts)
		if projectErr != nil {
			return toolcontract.Result{}, projectErr
		}
		if strings.TrimSpace(prompt) == "" {
			return toolcontract.Result{}, fmt.Errorf("%w: empty task question", ErrInvalidResult)
		}
		question, err = jsonv2.Marshal(prompt)
		if err == nil {
			schema, err = corejsonschema.Parse([]byte(`{"type":"string","minLength":1}`))
		}
	}
	if err != nil {
		return toolcontract.Result{}, err
	}
	state, err := jsonv2.Marshal(remoteTurn{Binding: r.binding, Original: sha256.Sum256(inv.Arguments()), Task: task.ID, Context: task.ContextID, Reply: rand.Text(), Wait: wait})
	if err != nil {
		return toolcontract.Result{}, err
	}
	request, err := toolcontract.NewInputRequest(toolcontract.InputRequestConfig{Prompt: question, ResponseSchema: schema, ContinuationState: state})
	if err != nil {
		return toolcontract.Result{}, err
	}
	return toolcontract.Result{InputRequest: &request}, nil
}

func (r remoteTool) reply(inv toolcontract.Invocation, c toolcontract.Continuation) (*sdka2a.Message, error) {
	var turn remoteTurn
	if err := jsonv2.Unmarshal(c.State(), &turn, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, errors.Join(ErrContinuationBinding, err)
	}
	if turn.Binding != r.binding || turn.Original != sha256.Sum256(inv.Arguments()) || turn.Task == "" || turn.Context == "" || turn.Reply == "" || turn.Wait != (WaitAddress{}) && !turn.Wait.Valid() {
		return nil, ErrContinuationBinding
	}
	var answer string
	var message *sdka2a.Message
	if err := jsonv2.Unmarshal(c.Response(), &answer); err == nil && string(c.Response()) != "null" {
		message = (textProjection{}).userMessage(answer)
	} else if turn.Wait.Valid() {
		p := sdka2a.NewTextPart(string(c.Response()))
		p.MediaType = "application/json"
		message = sdka2a.NewMessage(sdka2a.MessageRoleUser, p)
	} else {
		return nil, ErrUnsupportedContent
	}
	if turn.Wait.Valid() {
		address, err := turn.Wait.Metadata()
		if err != nil {
			return nil, err
		}
		message.Metadata = map[string]any{WaitMetadataKey: address}
	}
	message.TaskID = turn.Task
	message.ContextID = turn.Context
	message.ID = turn.Reply
	return message, nil
}
