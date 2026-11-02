package agent

import (
	"fmt"
	"strings"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

// DelegateConfig exposes one exact child Program as a model-selectable
// Interaction capability. Name and Description are written for the model;
// lifecycle identity and resource authority remain frozen Framework values.
type DelegateConfig struct {
	// Name is the provider-compatible model Tool name.
	Name string

	Description string

	// Program is the exact child program. An Interaction
	// configured with this Delegate owns it as one of its ChildPrograms.
	Program run.Program[run.Payload, run.Payload]

	// Budget is permanently allocated from the parent for each invocation.
	Budget run.Budget

	Capabilities run.CapabilitySet
}

// Delegate is an immutable, model-visible binding to one exact managed child
// Program. It is a composition value owned by Interaction, not an
// executable Tool or a second Process-start entry point.
type Delegate struct {
	definition   toolcontract.Definition
	deployment   run.Program[run.Payload, run.Payload]
	budget       run.Budget
	capabilities run.CapabilitySet
}

// NewDelegate fixes the target at construction; model selection cannot change
// its Program, budget, or capabilities.
func NewDelegate(config DelegateConfig) (Delegate, error) {
	if !config.Program.Valid() || !run.ValidDescription(config.Description) {
		return Delegate{}, ErrInvalidDelegate
	}
	descriptor := config.Program.Descriptor()
	if err := chat.ValidateToolName(config.Name); err != nil {
		return Delegate{}, fmt.Errorf("%w: %w", ErrInvalidDelegate, err)
	}
	definition := toolcontract.Definition{
		Name: config.Name, Description: config.Description,
		InputSchema: descriptor.InputSchema().JSON(),
	}
	if err := definition.Validate(); err != nil {
		return Delegate{}, fmt.Errorf("%w: model contract: %w", ErrInvalidDelegate, err)
	}
	return Delegate{
		definition: definition, deployment: config.Program,
		budget:       config.Budget,
		capabilities: config.Capabilities,
	}, nil
}

func (d Delegate) Valid() bool {
	return d.definition.Validate() == nil && run.ValidDescription(d.definition.Description) &&
		d.deployment.Valid()
}

func (d Delegate) validateInput(input run.Payload) error {
	if !d.Valid() {
		return ErrInvalidDelegate
	}
	return d.deployment.Descriptor().InputSchema().Validate(input.JSON())
}

// validateOutput is the one OutputSchema admission a successful Delegate result
// must pass. The current round and the archived records share it, so an output
// that cannot continue is rejected the first time Restore sees it, not later.
func (d Delegate) validateOutput(output run.Payload) error {
	if !d.Valid() {
		return ErrInvalidDelegate
	}
	if !output.Valid() {
		return fmt.Errorf("%w: Delegate result output is invalid", ErrInvalidDelegate)
	}
	return d.deployment.Descriptor().OutputSchema().Validate(output.JSON())
}

// admitResultOutput is validateOutput for a raw result: a success carrying no
// structured Details is rejected here, before it reaches the model context or an
// archived record.
func (d Delegate) admitResultOutput(output toolcontract.Output) (run.Payload, error) {
	payload, err := run.ParsePayload(output.Details)
	if err != nil {
		return run.Payload{}, fmt.Errorf("Delegate result output: %w", err)
	}
	if err := d.validateOutput(payload); err != nil {
		return run.Payload{}, err
	}
	return payload, nil
}

func delegateToolResult(call chat.ToolCall, result run.Result[run.Payload]) (chat.ToolResult, error) {
	if result.Termination().Status() != run.StatusCompleted {
		termination := result.Termination()
		diagnostic := "child ended with " + result.Termination().Status().String() + " (" + termination.Cause().String() + ")"
		if termination.Reason() != "" {
			diagnostic += ": " + termination.Reason()
		}
		return delegateErrorResult(call, diagnostic), nil
	}
	// A validated Completed termination always carries Output.
	output, _ := result.Termination().Output()
	toolOutput, err := toolcontract.NewJSONOutput(output.JSON())
	if err != nil {
		return chat.ToolResult{}, fmt.Errorf("%w: encode Delegate Tool output: %w", ErrInvalidExecutionState, err)
	}
	return chat.ToolResult{ID: call.ID, Name: call.Name, Output: toolOutput}, nil
}

func delegateErrorResult(call chat.ToolCall, diagnostic string) chat.ToolResult {
	return chat.ToolResult{
		ID: call.ID, Name: call.Name,
		Output: toolcontract.NewTextOutput("error: delegated worker " + run.NormalizeDiagnostic(diagnostic)), IsError: true,
	}
}

// rejectedDelegateStartResult is shared by live execution and SettledResults so
// both report the same model-visible refusal.
func rejectedDelegateStartResult(call chat.ToolCall, failure run.Failure) *toolCallResult {
	result := delegateErrorResult(call, "child start failed: "+failure.Code()+": "+failure.Message())
	completion := newToolCallResult(result)
	completion.Disposition = ResultRejected
	return &completion
}

func (d Delegate) prepareInput(call chat.ToolCall) (run.Payload, error) {
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	input, err := run.ParsePayload([]byte(arguments))
	if err != nil {
		return run.Payload{}, fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	if err := d.validateInput(input); err != nil {
		return run.Payload{}, fmt.Errorf("arguments violate the delegated worker input contract: %w", err)
	}
	return input, nil
}
