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
	acknowledger     ProcessInitializationOutcomeAcknowledger
	resolver         DeploymentResolver
	parentDeployment Deployment
	spec             ChildSpec
	childID          ProcessID
	relation         ProcessRelation
	limits           Limits
	treeLimits       TreeLimits
	requestDigest    Digest
}

type childStartJobResult struct {
	result     ChildStartResult
	deployment Deployment
	execution  Execution
	state      ExecutionState
	startedAt  time.Time
}

func (c childStartJobResult) started() bool {
	_, failed := c.result.Failure()
	return !failed && c.deployment.Valid() && c.execution != nil &&
		c.state.Valid() && !c.startedAt.IsZero()
}

func (c *childStartPlan) execute(ctx context.Context) childStartJobResult {
	deployment, resolveErr := c.resolveDeployment()
	if resolveErr != nil {
		return childStartJobResult{result: failedChildStart(
			c.spec, FailureKindExternal, failureCodeEngineChildDeploymentUnavailable, resolveErr,
		)}
	}
	if validateErr := deployment.Descriptor().ValidateInput(c.spec.Input); validateErr != nil {
		return childStartJobResult{result: failedChildStart(
			c.spec, FailureKindContract, failureCodeEngineChildInputInvalid, validateErr,
		)}
	}
	admission := newProcessAdmission(c.relation, deployment, c.spec.Budget, c.spec.Capabilities)
	if admissionErr := requestProcessAdmission(ctx, c.admitter, admission); admissionErr != nil {
		return childStartJobResult{result: failedChildStart(
			c.spec, FailureKindExternal, failureCodeEngineChildAdmissionRejected, admissionErr,
		)}
	}
	startedAt := time.Now().Round(0).UTC()
	execution, state, failure, err := initializeExecution(ctx, deployment.Definition(), c.spec.Input)
	if err != nil {
		acknowledgeErr := acknowledgeProcessInitializationOutcome(ctx, c.acknowledger, failedProcessInitializationOutcome(admission, failure))
		return childStartJobResult{result: failedChildStart(
			c.spec, failure.Kind(), failure.Code(), errors.Join(err, acknowledgeErr),
		)}
	}
	if err := acknowledgeProcessInitializationOutcome(ctx, c.acknowledger, initializedProcessOutcome(admission, startedAt)); err != nil {
		return childStartJobResult{result: failedChildStart(
			c.spec, FailureKindExternal, failureCodeEngineChildInitializationOutcomeUnacknowledged, err,
		)}
	}
	return childStartJobResult{
		result: ChildStartResult{
			key: c.spec.Key, processID: c.childID, deploymentRef: c.spec.DeploymentRef,
		},
		deployment: deployment, execution: execution, state: state, startedAt: startedAt,
	}
}

func (c *childStartPlan) resolveDeployment() (Deployment, error) {
	reference := c.spec.DeploymentRef
	if reference == c.parentDeployment.DeploymentRef() {
		return c.parentDeployment, c.parentDeployment.validateDefinition()
	}
	if c.resolver == nil {
		return Deployment{}, fmt.Errorf("%w: no resolver for %s", ErrInvalidDeployment, reference.Name())
	}
	deployment, err := resolveDeployment(c.resolver, reference)
	if err != nil {
		return Deployment{}, err
	}
	if !deployment.Valid() || deployment.DeploymentRef() != reference {
		return Deployment{}, fmt.Errorf("%w: resolver returned a different binding", ErrInvalidDeployment)
	}
	return deployment, nil
}

func resolveDeployment(
	resolver DeploymentResolver,
	reference DeploymentRef,
) (deployment Deployment, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			deployment = Deployment{}
			err = fmt.Errorf("deployment resolver panicked: %v", recovered)
		}
	}()
	deployment, err = resolver.Resolve(reference)
	if err != nil {
		return Deployment{}, err
	}
	if err := deployment.validateDefinition(); err != nil {
		return Deployment{}, err
	}
	return deployment, nil
}

func failedChildStart(spec ChildSpec, kind FailureKind, code string, cause error) ChildStartResult {
	return ChildStartResult{
		key: spec.Key, deploymentRef: spec.DeploymentRef,
		failure: newEngineFailure(kind, code, cause),
	}
}
