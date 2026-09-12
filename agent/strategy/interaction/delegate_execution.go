package interaction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func (e *execution) prepareDelegateChildren(ctx context.Context, calls []chat.ToolCall) ([]agent.Effect, error) {
	start := e.state.ToolRound.nextCallIndex()
	end := start
	for end < uint32(len(calls)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, delegated := e.definition.delegate(calls[end].Name); !delegated {
			break
		}
		end++
	}
	batch := &childCallBatch{Kind: childCallsDelegate,
		Invocations: make([]childInvocationState, end-start), NextStartIndex: end - start}
	effects := make([]agent.Effect, 0, len(batch.Invocations))
	for index := range batch.Invocations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		call := calls[start+uint32(index)]
		delegate, _ := e.definition.delegate(call.Name)
		arguments := strings.TrimSpace(call.Arguments)
		if arguments == "" {
			arguments = "{}"
		}
		input, err := agent.ParseInput([]byte(arguments))
		if err != nil {
			result := delegateErrorResult(call, "arguments are not valid JSON: "+err.Error())
			batch.Invocations[index].Result = &toolCallResult{Result: result}
			continue
		}
		if inputErr := delegate.validateInput(input); inputErr != nil {
			result := delegateErrorResult(call, "arguments violate the delegated worker input contract: "+inputErr.Error())
			batch.Invocations[index].Result = &toolCallResult{Result: result}
			continue
		}
		key, err := DelegateChildKey(e.state.ModelCallCount, call)
		if err != nil {
			return nil, err
		}
		effect, err := agent.StartChild(agent.ChildSpec{
			Key: key, DeploymentRef: delegate.deploymentRef, Input: input,
			Budget: delegate.budget, Capabilities: delegate.capabilities,
		})
		if err != nil {
			return nil, err
		}
		batch.Invocations[index].ChildKey = &key
		effects = append(effects, effect)
	}
	e.state.ToolRound.beginChildren(batch)
	return effects, nil
}

func (e *execution) acceptDelegateOutcome(index int, call chat.ToolCall, result agent.Result) error {
	if result.Status() != agent.StatusCompleted {
		termination := result.Termination()
		diagnostic := "child ended with " + result.Status().String() + " (" + termination.Cause().String() + ")"
		if termination.Reason() != "" {
			diagnostic += ": " + termination.Reason()
		}
		toolResult := delegateErrorResult(call, diagnostic)
		e.state.ToolRound.ChildBatch.Invocations[index].Result = &toolCallResult{Result: toolResult}
		return nil
	}
	output, present := result.Output()
	delegate, found := e.definition.delegate(call.Name)
	if !present || !found || delegate.outputSchema.ValidateOutput(output) != nil {
		return fmt.Errorf("%w: Delegate child output violates its frozen contract", ErrInvalidExecutionState)
	}
	toolOutput, err := chat.NewJSONToolOutput(output.JSON())
	if err != nil {
		return fmt.Errorf("%w: encode Delegate Tool output: %w", ErrInvalidExecutionState, err)
	}
	e.state.ToolRound.ChildBatch.Invocations[index].Result = &toolCallResult{Result: chat.ToolResult{
		ID: call.ID, Name: call.Name, Output: toolOutput,
	}}
	e.state.ArtifactRecords = append(e.state.ArtifactRecords, artifactRecord{
		ModelCallSequence: e.state.ModelCallCount,
		ToolCallIndex:     e.state.ToolRound.nextCallIndex() + uint32(index),
		ToolCallID:        call.ID, DelegateName: call.Name, Output: output,
	})
	return nil
}

func delegateErrorResult(call chat.ToolCall, diagnostic string) chat.ToolResult {
	return chat.ToolResult{
		ID: call.ID, Name: call.Name,
		Output: chat.NewTextToolOutput("error: delegated worker " + boundedDiagnostic(diagnostic)), IsError: true,
	}
}

// DelegateChildKey derives the exact managed ChildKey used for one Delegate
// ToolCall. Consumers can use the same value to correlate model observation
// with the child Process without exposing ToolCall to the Kernel.
func DelegateChildKey(modelCallSequence uint32, toolCall chat.ToolCall) (agent.ChildKey, error) {
	if modelCallSequence == 0 {
		return agent.ChildKey{}, fmt.Errorf("%w: model call sequence is required", ErrInvalidDelegate)
	}
	if err := toolCall.Validate(); err != nil {
		return agent.ChildKey{}, fmt.Errorf("%w: ToolCall: %w", ErrInvalidDelegate, err)
	}
	hash := sha256.New()
	hash.Write([]byte(strconv.FormatUint(uint64(modelCallSequence), 10)))
	hash.Write([]byte{0})
	hash.Write([]byte(toolCall.ID))
	hash.Write([]byte{0})
	hash.Write([]byte(toolCall.Name))
	return agent.ParseChildKey("interaction.delegate.child." + hex.EncodeToString(hash.Sum(nil)))
}
