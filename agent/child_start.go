package agent

import (
	"context"
	"errors"
	"fmt"
)

// childStartPreparation either plans a start or records why it failed.
type childStartPreparation struct {
	plan    *childStartPlan
	failure Failure
}

type childStartPlan struct {
	admitter         ProcessAdmitter
	acknowledger     ProcessInitializationAcknowledger
	resolver         DeploymentResolver
	parentDeployment Deployment
	spec             ChildSpec
	relation         ProcessRelation
}

func (c *childStartPlan) childID() ProcessID { return c.relation.ProcessID() }

func (c *childStartPlan) parentID() ProcessID {
	parentID, _ := c.relation.ParentID()
	return parentID
}

// childStartJobResult carries the failure of an unsuccessful start; a started
// child's identity follows from the Effect, not from this result. The child's
// start time is stamped at publication, so it is not carried here.
type childStartJobResult struct {
	failure    Failure
	deployment Deployment
	execution  Execution
	state      ExecutionState
}

func (childStartJobResult) jobKind() processJobKind { return processJobChildStart }

// started is the absence of a failure; execute supplies the initialized
// child exactly when it reports none.
func (c childStartJobResult) started() bool { return !c.failure.Valid() }

func (c *childStartPlan) execute(ctx context.Context) childStartJobResult {
	deployment, resolveErr := c.resolveDeployment()
	if resolveErr != nil {
		return childStartJobResult{failure: newEngineFailure(failureKindForError(resolveErr, FailureKindExternal), failureCodeEngineChildDeploymentUnavailable, resolveErr)}
	}
	if validateErr := deployment.Descriptor().ValidateInput(c.spec.Input); validateErr != nil {
		return childStartJobResult{failure: newEngineFailure(FailureKindContract, failureCodeEngineChildInputInvalid, validateErr)}
	}
	admission := newProcessAdmission(c.relation, deployment, c.spec.Budget, c.spec.Capabilities)
	if admissionErr := requestProcessAdmission(ctx, c.admitter, admission); admissionErr != nil {
		return childStartJobResult{failure: newEngineFailure(failureKindForError(admissionErr, FailureKindExternal), failureCodeEngineChildAdmissionRejected, admissionErr)}
	}
	execution, state, failure, err := initializeExecution(ctx, deployment.Definition(), c.spec.Input)
	if err != nil {
		acknowledgeErr := acknowledgeProcessInitialization(ctx, c.acknowledger, failedProcessInitializationOutcome(admission, failure))
		return childStartJobResult{failure: newEngineFailure(failure.Kind(), failure.Code(), errors.Join(err, acknowledgeErr))}
	}
	if err := acknowledgeProcessInitialization(ctx, c.acknowledger, initializedProcessOutcome(admission)); err != nil {
		return childStartJobResult{failure: newEngineFailure(failureKindForError(err, FailureKindExternal), failureCodeEngineChildInitializationOutcomeUnacknowledged, err)}
	}
	return childStartJobResult{
		deployment: deployment, execution: execution, state: state,
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
