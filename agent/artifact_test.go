package agent_test

import (
	startrand "crypto/rand"

	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestCompletionValidatorUsesOrderedTypedDelegateArtifacts(t *testing.T) {
	child := delegateWorkflow(t, "interaction.artifact_worker", func(_ context.Context, input delegateRequest) (delegateResponse, error) {
		return delegateResponse{Value: "artifact:" + input.Value}, nil
	})
	budget := run.Budget{Steps: run.NewQuota(20), Effects: run.NewQuota(20), Signals: run.NewQuota(20)}
	delegate, err := agent.NewDelegate(agent.DelegateConfig{
		Name: "delegate_artifact", Description: "Produce one typed artifact for completion validation.",
		Program: child, Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &artifactValidationModel{}
	root := delegateInteractionWithValidator(
		t, model, nil, []agent.Delegate{delegate}, validateArtifactCompletion, 4,
	)
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(context.Background(), engine, root.Program, interactionInput(t, "produce validated evidence"))
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
		t.Fatal(closeErr)
	}
	if result.Termination().Status() != run.StatusCompleted || model.Calls() != 3 {
		t.Fatalf("status=%s model calls=%d", result.Termination().Status(), model.Calls())
	}
	erased, _ := result.Termination().Output()
	output, err := erased.Decode[agent.Output]()
	if err != nil || output.ModelResponse == nil || output.ModelResponse.Text() != "artifact:evidence supports the answer" {
		t.Fatalf("output=%#v error=%v", output, err)
	}
}

func validateArtifactCompletion(_ context.Context, candidate agent.CompletionCandidate) (agent.CompletionDecision, error) {
	for _, artifact := range candidate.Artifacts() {
		if artifact.ModelCallSequence() != 1 || artifact.ToolCallID() != "call_artifact" {
			return agent.CompletionDecision{}, errors.New("artifact lost call identity")
		}
	}
	if err := validateCandidateOwnership(candidate); err != nil {
		return agent.CompletionDecision{}, err
	}
	artifact, err := validatedArtifact(candidate)
	if err != nil {
		return agent.CompletionDecision{}, err
	}
	if payloadErr := validateArtifactPayload(artifact); payloadErr != nil {
		return agent.CompletionDecision{}, payloadErr
	}
	candidateText, err := validateOutputOwnership(candidate)
	if err != nil {
		return agent.CompletionDecision{}, err
	}
	if candidateText == "premature" {
		return agent.CompletionDecision{
			Feedback: "The delegated evidence is present, but the final answer must cite it explicitly.",
		}, nil
	}
	return agent.CompletionDecision{Accepted: true}, nil
}

func validateCandidateOwnership(candidate agent.CompletionCandidate) error {
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

func validatedArtifact(candidate agent.CompletionCandidate) (agent.Artifact, error) {
	artifacts := candidate.Artifacts()
	if len(artifacts) != 1 {
		return agent.Artifact{}, fmt.Errorf("artifact count = %d", len(artifacts))
	}
	artifact := artifacts[0]
	if artifact.DelegateName() != "delegate_artifact" {
		return agent.Artifact{}, errors.New("artifact Delegate identity is incorrect")
	}
	artifacts[0] = agent.Artifact{}
	if candidate.Artifacts()[0].DelegateName() != "delegate_artifact" {
		return agent.Artifact{}, errors.New("Artifact snapshot aliases validator-owned slice")
	}
	return artifact, nil
}

func validateArtifactPayload(artifact agent.Artifact) error {
	decoded, err := artifact.Output().Decode[delegateResponse]()
	if err != nil {
		return fmt.Errorf("decode artifact: %w", err)
	}
	if decoded.Value != "artifact:evidence" {
		return fmt.Errorf("decoded artifact = %#v", decoded)
	}
	_, mismatchErr := artifact.Output().Decode[struct {
		Other string `json:"other"`
	}]()
	if !errors.Is(mismatchErr, run.ErrInvalidPayload) {
		return fmt.Errorf("wrong typed decode error: %w", mismatchErr)
	}
	return nil
}

func validateOutputOwnership(candidate agent.CompletionCandidate) (string, error) {
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
		func(_ context.Context, _ agent.CompletionCandidate) (agent.CompletionDecision, error) {
			return agent.CompletionDecision{Feedback: "Produce a verifiable final answer."}, nil
		},
		1,
	)
	engine, _ := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	result, err := runtest.Observe(context.Background(), engine, root.Program, interactionInput(t, "bounded validation"))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	failure, present := result.Termination().Failure()
	if result.Termination().Status() != run.StatusFailed || !present ||
		failure.Code() != "interaction.limit.model_calls" || failure.Kind() != run.FailureKindExecution {
		t.Fatalf("termination=%#v", result.Termination())
	}
}

func TestCompletionDecisionContract(t *testing.T) {
	cases := []struct {
		name     string
		decision agent.CompletionDecision
		valid    bool
	}{
		{name: "accepted", decision: agent.CompletionDecision{Accepted: true}, valid: true},
		{name: "retry", decision: agent.CompletionDecision{Feedback: "Add evidence."}, valid: true},
		{name: "invalid UTF-8", decision: agent.CompletionDecision{Feedback: "\xff"}},
		{name: "Chinese", decision: agent.CompletionDecision{Feedback: "补充证据"}, valid: true},
		{name: "byte boundary", decision: agent.CompletionDecision{Feedback: strings.Repeat("界", 1365) + "a"}, valid: true},
		{name: "byte overflow", decision: agent.CompletionDecision{Feedback: strings.Repeat("界", 1366)}},
		{name: "zero", decision: agent.CompletionDecision{}},
		{name: "accepted with feedback", decision: agent.CompletionDecision{Accepted: true, Feedback: "unused"}},
		{name: "untrimmed feedback", decision: agent.CompletionDecision{Feedback: " retry"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := test.decision.Valid(); got != test.valid {
				t.Fatalf("Valid()=%t, want %t", got, test.valid)
			}
		})
	}
	if _, err := (agent.Artifact{}).Output().Decode[delegateResponse](); !errors.Is(err, run.ErrInvalidPayload) {
		t.Fatalf("zero Artifact payload decode error=%v", err)
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
	validator := func(_ context.Context, candidate agent.CompletionCandidate) (agent.CompletionDecision, error) {
		if len(candidate.Artifacts()) != 0 {
			return agent.CompletionDecision{}, errors.New("ordinary Tool produced a Delegate Artifact")
		}
		if len(candidate.Output().DirectToolResults) != 0 {
			return agent.CompletionDecision{Feedback: "Explain the direct result before completing."}, nil
		}
		return agent.CompletionDecision{Accepted: true}, nil
	}
	root := delegateInteractionWithValidator(
		t, model, []tool.Tool{directTool{Tool: echo}}, nil, validator, 3,
	)
	engine, _ := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	result, err := runtest.Observe(context.Background(), engine, root.Program, interactionInput(t, "echo and explain"))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if result.Termination().Status() != run.StatusCompleted || model.Calls() != 2 {
		t.Fatalf("status=%s calls=%d", result.Termination().Status(), model.Calls())
	}
}

func TestCompletionValidatorRejectsInvalidDecision(t *testing.T) {
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return textResponse("candidate"), nil
	})
	root := delegateInteractionWithValidator(
		t, model, nil, nil,
		func(_ context.Context, _ agent.CompletionCandidate) (agent.CompletionDecision, error) {
			return agent.CompletionDecision{Feedback: "\xff"}, nil
		},
		2,
	)
	engine, _ := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	result, err := runtest.Observe(context.Background(), engine, root.Program, interactionInput(t, "invalid validator"))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	failure, present := result.Termination().Failure()
	if result.Termination().Status() != run.StatusFailed || result.Termination().Cause() != run.TerminationCauseContractFailure ||
		!present || failure.Kind() != run.FailureKindContract ||
		failure.Code() != "interaction.completion.decision_invalid" {
		t.Fatalf("termination=%#v", result.Termination())
	}
}

func TestCompletionValidatorFailureClassification(t *testing.T) {
	cases := []struct {
		name      string
		validator agent.CompletionValidator
		cause     run.TerminationCause
		kind      run.FailureKind
		code      string
		message   string
	}{
		{
			name: "returned error",
			validator: func(_ context.Context, _ agent.CompletionCandidate) (agent.CompletionDecision, error) {
				return agent.CompletionDecision{}, errors.New("validator cannot decide")
			},
			cause: run.TerminationCauseExecutionFailure,
			kind:  run.FailureKindExecution,
			code:  "interaction.completion.validator_failed",
		},
		{
			name: "invalid error encoding",
			validator: func(_ context.Context, _ agent.CompletionCandidate) (agent.CompletionDecision, error) {
				return agent.CompletionDecision{}, errors.New("failure: \xff")
			},
			cause: run.TerminationCauseExecutionFailure, kind: run.FailureKindExecution,
			code: "interaction.completion.validator_failed", message: "failure: \ufffd",
		},
		{
			name: "long invalid error encoding",
			validator: func(_ context.Context, _ agent.CompletionCandidate) (agent.CompletionDecision, error) {
				return agent.CompletionDecision{}, errors.New("\xff" + strings.Repeat("a", run.MaxDiagnosticBytes))
			},
			cause: run.TerminationCauseExecutionFailure, kind: run.FailureKindExecution,
			code: "interaction.completion.validator_failed", message: "\ufffd" + strings.Repeat("a", run.MaxDiagnosticBytes-3),
		},
		{
			name: "panic",
			validator: func(_ context.Context, _ agent.CompletionCandidate) (agent.CompletionDecision, error) {
				panic("validator panic")
			},
			cause: run.TerminationCausePanic,
			kind:  run.FailureKindPanic,
			code:  "execution.step.failed",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				return textResponse("candidate"), nil
			})
			root := delegateInteractionWithValidator(t, model, nil, nil, test.validator, 2)
			engine, _ := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
			result, err := runtest.Observe(context.Background(), engine, root.Program, interactionInput(t, "validator failure"))
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

func TestCompletionValidatorObservesStepCancellation(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return textResponse("candidate"), nil })
	root := delegateInteractionWithValidator(t, model, nil, nil, func(ctx context.Context, _ agent.CompletionCandidate) (agent.CompletionDecision, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		return agent.CompletionDecision{}, ctx.Err()
	}, 1)
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(context.WithoutCancel(t.Context()))
	process, err := engine.Start(t.Context(), root.Program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "cancel validation")})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if killErr := process.Kill(t.Context(), "cancel validator"); killErr != nil {
		t.Fatal(killErr)
	}
	<-canceled
	result, err := process.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusKilled {
		t.Fatalf("status=%s error=%v", result.Termination().Status(), err)
	}
	if joinErr := process.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
}
