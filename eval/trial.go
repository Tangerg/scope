package eval

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"
)

var ErrInvalidTrial = errors.New("eval: invalid trial")

// TrialSample keeps Execution apart from Case so generated outputs never change
// fixture identity and can be regraded without invoking the target again.
type TrialSample[I, O any] struct {
	Case      Case[I]
	Execution Execution[O]
}

// TrialConfig requires both fields; share them only when they support the
// Host's concurrent calls, because Trial adds no scheduling of its own.
type TrialConfig[I, O any] struct {
	Target Target[I, O]
	Suite  *Suite[TrialSample[I, O]]
}

// Trial composes one execution with independent assessments. Dataset
// scheduling, retries, persistence, and sandboxes belong to the Host.
type Trial[I, O any] struct {
	target Target[I, O]
	suite  *Suite[TrialSample[I, O]]
}

func NewTrial[I, O any](config TrialConfig[I, O]) (*Trial[I, O], error) {
	if lo.IsNil(config.Target) {
		return nil, fmt.Errorf("%w: target is nil", ErrInvalidTrial)
	}
	if config.Suite == nil || len(config.Suite.assessments) == 0 {
		return nil, fmt.Errorf("%w: suite is uninitialized", ErrInvalidTrial)
	}
	return &Trial[I, O]{target: config.Target, suite: config.Suite}, nil
}

// Run returns every accumulated stage result: a valid execution survives failed
// or canceled grading, and an ExecutionError leaves assessments not evaluated.
// An execution without output is still graded; assessments decide which
// evidence they require. The result holds runtime errors and is not a storage
// schema.
func (t *Trial[I, O]) Run(ctx context.Context, caseValue Case[I]) (TrialResult[O], error) {
	if t == nil || lo.IsNil(t.target) || t.suite == nil {
		return TrialResult[O]{}, fmt.Errorf("%w: uninitialized trial", ErrInvalidTrial)
	}
	if err := caseValue.Validate(); err != nil {
		return TrialResult[O]{}, err
	}
	result := TrialResult[O]{Case: CaseResult{
		ID: caseValue.ID, Metadata: caseValue.Metadata.Clone(), Result: t.suite.unevaluated(nil),
	}}
	if err := ctx.Err(); err != nil {
		result.ExecutionError = err
		result.Case.Result = t.suite.unevaluated(err)
		return result, err
	}
	execution, err := t.target.Run(ctx, caseValue.Subject)
	if err == nil {
		err = execution.Validate()
	}
	if err != nil {
		result.ExecutionError = err
		return result, err
	}
	execution = execution.snapshot()
	result.Execution = &execution
	result.Case.Result, err = t.suite.Run(ctx, TrialSample[I, O]{Case: caseValue.clone(), Execution: execution})
	return result, err
}

// TrialResult preserves execution and assessment outcomes independently. Case
// can be included in NewExperimentReport with other cases from the same fixture.
// ExecutionError is an invocation/protocol error, never a quality judgment.
type TrialResult[O any] struct {
	Case           CaseResult
	Execution      *Execution[O]
	ExecutionError error
}
