package interaction_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestCompletionValidatorUsesOrderedTypedDelegateArtifacts(t *testing.T) {
	child := delegateWorkflow(t, "interaction.artifact_worker", func(_ context.Context, input delegateRequest) (delegateResponse, error) {
		return delegateResponse{Value: "artifact:" + input.Value}, nil
	})
	budget := agent.Budget{Steps: 20, Effects: 20, Signals: 20}
	delegate, err := interaction.NewDelegate(interaction.DelegateConfig{
		Name: "delegate_artifact", Description: "Produce one typed artifact for completion validation.",
		Deployment: child, Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &artifactValidationModel{}
	root := delegateInteractionWithValidator(
		t, model, nil, []interaction.Delegate{delegate}, validateArtifactCompletion, 4,
	)
	engine, err := agent.NewEngine(agent.EngineConfig{
		DeploymentResolver: root.resolveWith(child),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), root.Deployment, interactionInput(t, "produce validated evidence"))
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
		t.Fatal(closeErr)
	}
	if result.Status() != agent.StatusCompleted || model.Calls() != 3 {
		t.Fatalf("status=%s model calls=%d", result.Status(), model.Calls())
	}
	erased, _ := result.Output()
	output, err := erased.Decode[interaction.Output]()
	if err != nil || output.ModelResponse == nil || output.ModelResponse.Text() != "artifact:evidence supports the answer" {
		t.Fatalf("output=%#v error=%v", output, err)
	}
}

func validateArtifactCompletion(candidate interaction.CompletionCandidate) (interaction.CompletionDecision, error) {
	if err := validateCandidateOwnership(candidate); err != nil {
		return interaction.CompletionDecision{}, err
	}
	artifact, err := validatedArtifact(candidate)
	if err != nil {
		return interaction.CompletionDecision{}, err
	}
	if payloadErr := validateArtifactPayload(artifact); payloadErr != nil {
		return interaction.CompletionDecision{}, payloadErr
	}
	candidateText, err := validateOutputOwnership(candidate)
	if err != nil {
		return interaction.CompletionDecision{}, err
	}
	if candidateText == "premature" {
		return interaction.CompletionDecision{
			Feedback: "The delegated evidence is present, but the final answer must cite it explicitly.",
		}, nil
	}
	return interaction.CompletionDecision{Accepted: true}, nil
}

func validateCandidateOwnership(candidate interaction.CompletionCandidate) error {
	workingContext := candidate.WorkingContext()
	if workingContext == nil || len(workingContext.Messages) == 0 {
		return errors.New("candidate has no WorkingContext")
	}
	workingContext.Messages[0] = chat.NewUserMessage(chat.NewTextPart("mutated"))
	if candidate.WorkingContext().Messages[0].Text() == "mutated" {
		return errors.New("WorkingContext aliases validator-owned copy")
	}
	return nil
}

func validatedArtifact(candidate interaction.CompletionCandidate) (interaction.Artifact, error) {
	artifacts := candidate.Artifacts().All()
	if len(artifacts) != 1 {
		return interaction.Artifact{}, fmt.Errorf("artifact count = %d", len(artifacts))
	}
	artifact := artifacts[0]
	if artifact.DelegateName() != "delegate_artifact" {
		return interaction.Artifact{}, errors.New("artifact Delegate identity is incorrect")
	}
	artifacts[0] = interaction.Artifact{}
	if candidate.Artifacts().All()[0].DelegateName() != "delegate_artifact" {
		return interaction.Artifact{}, errors.New("Artifact snapshot aliases validator-owned slice")
	}
	return artifact, nil
}

func validateArtifactPayload(artifact interaction.Artifact) error {
	decoded, err := artifact.Decode[delegateResponse]()
	if err != nil {
		return fmt.Errorf("decode artifact: %w", err)
	}
	if decoded.Value != "artifact:evidence" {
		return fmt.Errorf("decoded artifact = %#v", decoded)
	}
	_, mismatchErr := artifact.Decode[struct {
		Other string `json:"other"`
	}]()
	if !errors.Is(mismatchErr, interaction.ErrInvalidArtifact) || !errors.Is(mismatchErr, agent.ErrInvalidOutput) {
		return fmt.Errorf("wrong typed decode error: %w", mismatchErr)
	}
	return nil
}

func validateOutputOwnership(candidate interaction.CompletionCandidate) (string, error) {
	output := candidate.Output()
	if output.ModelResponse == nil {
		return "", nil
	}
	candidateText := output.ModelResponse.Text()
	output.ModelResponse.Output.Message = nil
	if candidate.Output().ModelResponse.Text() != candidateText {
		return "", errors.New("candidate Output aliases validator-owned copy")
	}
	return candidateText, nil
}

func TestCompletionValidatorRetryHonorsModelCallLimit(t *testing.T) {
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return textResponse("not accepted"), nil
	})
	root := delegateInteractionWithValidator(
		t, model, nil, nil,
		func(interaction.CompletionCandidate) (interaction.CompletionDecision, error) {
			return interaction.CompletionDecision{Feedback: "Produce a verifiable final answer."}, nil
		},
		1,
	)
	engine, _ := agent.NewEngine(agent.EngineConfig{DeploymentResolver: root.resolver})
	result, err := engine.Run(context.Background(), root.Deployment, interactionInput(t, "bounded validation"))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	failure, present := result.Termination().Failure()
	if result.Status() != agent.StatusFailed || !present ||
		failure.Code() != "interaction.limit.model_calls" || failure.Kind() != agent.FailureKindExecution {
		t.Fatalf("termination=%#v", result.Termination())
	}
}

func TestCompletionDecisionContract(t *testing.T) {
	cases := []struct {
		name     string
		decision interaction.CompletionDecision
		valid    bool
	}{
		{name: "accepted", decision: interaction.CompletionDecision{Accepted: true}, valid: true},
		{name: "retry", decision: interaction.CompletionDecision{Feedback: "Add evidence."}, valid: true},
		{name: "zero", decision: interaction.CompletionDecision{}},
		{name: "accepted with feedback", decision: interaction.CompletionDecision{Accepted: true, Feedback: "unused"}},
		{name: "untrimmed feedback", decision: interaction.CompletionDecision{Feedback: " retry"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := test.decision.Valid(); got != test.valid {
				t.Fatalf("Valid()=%t, want %t", got, test.valid)
			}
		})
	}
	if _, err := (interaction.Artifact{}).Decode[delegateResponse](); !errors.Is(err, interaction.ErrInvalidArtifact) {
		t.Fatalf("DecodeArtifact(zero) error=%v", err)
	}
}

func TestCompletionValidatorCanRejectDirectToolResult(t *testing.T) {
	type echoInput struct {
		Value string `json:"value"`
	}
	echo, err := tool.NewFunc(tool.FuncConfig{
		Name: "direct_echo", Description: "Return text as a direct semantic result.",
	}, func(_ context.Context, input echoInput) (string, error) { return input.Value, nil })
	if err != nil {
		t.Fatal(err)
	}
	model := &directCompletionValidationModel{}
	validator := func(candidate interaction.CompletionCandidate) (interaction.CompletionDecision, error) {
		if candidate.Artifacts().Len() != 0 {
			return interaction.CompletionDecision{}, errors.New("ordinary Tool produced a Delegate Artifact")
		}
		if candidate.Output().Source == interaction.CompletionSourceDirectToolResults {
			return interaction.CompletionDecision{Feedback: "Explain the direct result before completing."}, nil
		}
		return interaction.CompletionDecision{Accepted: true}, nil
	}
	root := delegateInteractionWithValidator(
		t, model, []tool.Tool{directTool{Tool: echo}}, nil, validator, 3,
	)
	engine, _ := agent.NewEngine(agent.EngineConfig{DeploymentResolver: root.resolver})
	result, err := engine.Run(context.Background(), root.Deployment, interactionInput(t, "echo and explain"))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted || model.Calls() != 2 {
		t.Fatalf("status=%s calls=%d", result.Status(), model.Calls())
	}
}

func TestCompletionValidatorRejectsInvalidDecision(t *testing.T) {
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return textResponse("candidate"), nil
	})
	root := delegateInteractionWithValidator(
		t, model, nil, nil,
		func(interaction.CompletionCandidate) (interaction.CompletionDecision, error) {
			return interaction.CompletionDecision{}, nil
		},
		2,
	)
	engine, _ := agent.NewEngine(agent.EngineConfig{DeploymentResolver: root.resolver})
	result, err := engine.Run(context.Background(), root.Deployment, interactionInput(t, "invalid validator"))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	failure, present := result.Termination().Failure()
	if result.Status() != agent.StatusFailed || result.Termination().Cause() != agent.TerminationCauseContractFailure ||
		!present || failure.Kind() != agent.FailureKindContract ||
		failure.Code() != "interaction.completion.decision_invalid" {
		t.Fatalf("termination=%#v", result.Termination())
	}
}

func TestCompletionValidatorFailureClassification(t *testing.T) {
	cases := []struct {
		name      string
		validator interaction.CompletionValidator
		cause     agent.TerminationCause
		kind      agent.FailureKind
		code      string
		message   string
	}{
		{
			name: "returned error",
			validator: func(interaction.CompletionCandidate) (interaction.CompletionDecision, error) {
				return interaction.CompletionDecision{}, errors.New("validator cannot decide")
			},
			cause: agent.TerminationCauseExecutionFailure,
			kind:  agent.FailureKindExecution,
			code:  "interaction.completion.validator_failed",
		},
		{
			name: "invalid error encoding",
			validator: func(interaction.CompletionCandidate) (interaction.CompletionDecision, error) {
				return interaction.CompletionDecision{}, errors.New("failure: \xff")
			},
			cause: agent.TerminationCauseExecutionFailure, kind: agent.FailureKindExecution,
			code: "interaction.completion.validator_failed", message: "failure: \ufffd",
		},
		{
			name: "long invalid error encoding",
			validator: func(interaction.CompletionCandidate) (interaction.CompletionDecision, error) {
				return interaction.CompletionDecision{}, errors.New("\xff" + strings.Repeat("a", 2048))
			},
			cause: agent.TerminationCauseExecutionFailure, kind: agent.FailureKindExecution,
			code: "interaction.completion.validator_failed", message: "\ufffd" + strings.Repeat("a", 2045),
		},
		{
			name: "panic",
			validator: func(interaction.CompletionCandidate) (interaction.CompletionDecision, error) {
				panic("validator panic")
			},
			cause: agent.TerminationCausePanic,
			kind:  agent.FailureKindPanic,
			code:  "execution.step.failed",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				return textResponse("candidate"), nil
			})
			root := delegateInteractionWithValidator(t, model, nil, nil, test.validator, 2)
			engine, _ := agent.NewEngine(agent.EngineConfig{DeploymentResolver: root.resolver})
			result, err := engine.Run(context.Background(), root.Deployment, interactionInput(t, "validator failure"))
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Fatal(err)
			}
			failure, present := result.Termination().Failure()
			if result.Termination().Cause() != test.cause || !present ||
				failure.Kind() != test.kind || failure.Code() != test.code {
				t.Fatalf("termination=%#v", result.Termination())
			}
			if test.message != "" && failure.Message() != test.message {
				t.Fatal("validator failure diagnostic changed or was discarded")
			}
		})
	}
}

type artifactValidationModel struct {
	mu    sync.Mutex
	calls int
}

func (a *artifactValidationModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	switch a.calls {
	case 1:
		return toolCallResponse(chat.ToolCall{
			ID: "call_artifact", Name: "delegate_artifact", Arguments: `{"value":"evidence"}`,
		}), nil
	case 2:
		return textResponse("premature"), nil
	case 3:
		if len(request.Messages) != 5 || request.Messages[3].Role != chat.RoleAssistant ||
			request.Messages[3].Text() != "premature" || request.Messages[4].Role != chat.RoleUser ||
			request.Messages[4].Text() != "The delegated evidence is present, but the final answer must cite it explicitly." {
			return nil, fmt.Errorf("completion feedback context = %#v", request.Messages)
		}
		return textResponse("artifact:evidence supports the answer"), nil
	default:
		return nil, errors.New("unexpected model call")
	}
}

func (a *artifactValidationModel) Calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

type directCompletionValidationModel struct {
	mu    sync.Mutex
	calls int
}

func (d *directCompletionValidationModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.calls == 1 {
		return toolCallResponse(chat.ToolCall{
			ID: "call_direct_validation", Name: "direct_echo", Arguments: `{"value":"direct"}`,
		}), nil
	}
	if len(request.Messages) != 4 || request.Messages[1].Role != chat.RoleAssistant ||
		request.Messages[2].Role != chat.RoleTool || request.Messages[3].Role != chat.RoleUser ||
		request.Messages[3].Text() != "Explain the direct result before completing." {
		return nil, fmt.Errorf("direct completion feedback context = %#v", request.Messages)
	}
	return textResponse("direct, explained"), nil
}

func (d *directCompletionValidationModel) Calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}
