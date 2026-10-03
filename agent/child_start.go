package agent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type childStartPreparation struct {
	plan   *childStartPlan
	result ChildStartResult
}

type childStartPlan struct {
	admitter         ProcessAdmitter
	acknowledger     ProcessInitializationAcknowledger
	resolver         DeploymentResolver
	parentDeployment Deployment
	spec             ChildSpec
	childID          ProcessID
	relation         ProcessRelation
	requestDigest    Digest
}

type childStartJobResult struct {
	result     ChildStartResult
	deployment Deployment
	execution  Execution
	state      ExecutionState
	startedAt  time.Time
}

func (childStartJobResult) jobKind() processJobKind { return processJobChildStart }

func (c childStartJobResult) started() bool {
	_, failed := c.result.Failure()
	return !failed && c.deployment.Valid() && c.execution != nil &&
		c.state.Valid() && !c.startedAt.IsZero()
}

func (c *childStartPlan) execute(ctx context.Context) childStartJobResult {
	deployment, resolveErr := c.resolveDeployment()
	if resolveErr != nil {
		return childStartJobResult{result: failedChildStart(failureKindForError(resolveErr, FailureKindExternal), failureCodeEngineChildDeploymentUnavailable, resolveErr)}
	}
	if validateErr := deployment.Descriptor().ValidateInput(c.spec.Input); validateErr != nil {
		return childStartJobResult{result: failedChildStart(FailureKindContract, failureCodeEngineChildInputInvalid, validateErr)}
	}
	admission := newProcessAdmission(c.relation, deployment, c.spec.Budget, c.spec.Capabilities)
	if admissionErr := requestProcessAdmission(ctx, c.admitter, admission); admissionErr != nil {
		return childStartJobResult{result: failedChildStart(failureKindForError(admissionErr, FailureKindExternal), failureCodeEngineChildAdmissionRejected, admissionErr)}
	}
	startedAt := canonicalTime(time.Now())
	execution, state, failure, err := initializeExecution(ctx, deployment.Definition(), c.spec.Input)
	if err != nil {
		acknowledgeErr := acknowledgeProcessInitialization(ctx, c.acknowledger, failedProcessInitializationOutcome(admission, failure))
		return childStartJobResult{result: failedChildStart(failure.Kind(), failure.Code(), errors.Join(err, acknowledgeErr))}
	}
	if err := acknowledgeProcessInitialization(ctx, c.acknowledger, initializedProcessOutcome(admission, startedAt)); err != nil {
		return childStartJobResult{result: failedChildStart(failureKindForError(err, FailureKindExternal), failureCodeEngineChildInitializationOutcomeUnacknowledged, err)}
	}
	return childStartJobResult{
		result:     ChildStartResult{processID: c.childID},
		deployment: deployment, execution: execution, state: state, startedAt: startedAt,
	}
}

func (c *childStartPlan) resolveDeployment() (Deployment, error) {
	reference := c.spec.DeploymentRef
	if reference == c.parentDeployment.DeploymentRef() {
		return c.parentDeployment, c.parentDeployment.validateDefinition()
	}
	if bound, found := c.parentDeployment.boundChild(reference); found {
		return bound, bound.validateDefinition()
	}
	if c.resolver == nil {
		return Deployment{}, fmt.Errorf("%w: no resolver for %s", ErrInvalidDeployment, reference.Name())
	}
	return resolveDeployment(c.resolver, reference)
}

func resolveDeployment(resolver DeploymentResolver, reference DeploymentRef) (Deployment, error) {
	deployment, err := invokeCallback("DeploymentResolver.Resolve", func() (Deployment, error) {
		return resolver.Resolve(reference)
	})
	if err != nil {
		return Deployment{}, err
	}
	if err := deployment.validateDefinition(); err != nil {
		return Deployment{}, err
	}
	if deployment.DeploymentRef() != reference {
		return Deployment{}, fmt.Errorf(
			"%w: resolver answered %s with a different binding", ErrInvalidDeployment, reference.Name(),
		)
	}
	return deployment, nil
}

func failedChildStart(kind FailureKind, code string, cause error) ChildStartResult {
	return ChildStartResult{failure: newEngineFailure(kind, code, cause)}
}
