package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	childRequestInvalidCode                      = "engine.child.request.invalid"
	childIdentityConflictCode                    = "engine.child.identity_conflict"
	childCapabilityEscalationCode                = "engine.child.capability_escalation"
	childBudgetExhaustedCode                     = "engine.child.budget_exhausted"
	childBudgetInvalidCode                       = "engine.child.budget_invalid"
	childTreeLimitCode                           = "engine.child.tree_limit"
	childStartUnavailableCode                    = "engine.child.start.unavailable"
	childStartInterruptedCode                    = "engine.child.start.interrupted"
	childDeploymentUnavailableCode               = "engine.child.deployment_unavailable"
	childInputInvalidCode                        = "engine.child.input.invalid"
	childAdmissionRejectedCode                   = "engine.child.admission.rejected"
	childInitializationOutcomeUnacknowledgedCode = "engine.child.initialization_outcome.unacknowledged"
	childSettlementInvalidCode                   = "engine.child.settlement.invalid"
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
			c.spec, FailureKindExternal, childDeploymentUnavailableCode, resolveErr,
		)}
	}
	if validateErr := deployment.Descriptor().ValidateInput(c.spec.Input); validateErr != nil {
		return childStartJobResult{result: failedChildStart(
			c.spec, FailureKindContract, childInputInvalidCode, validateErr,
		)}
	}
	admission := newProcessAdmission(c.relation, deployment, c.spec.Budget, c.spec.Capabilities)
	if admissionErr := requestProcessAdmission(ctx, c.admitter, admission); admissionErr != nil {
		return childStartJobResult{result: failedChildStart(
			c.spec, FailureKindExternal, childAdmissionRejectedCode, admissionErr,
		)}
	}
	startedAt := time.Now().Round(0).UTC()
	execution, state, failure, err := initializeExecution(deployment.Definition(), c.spec.Input)
	if err != nil {
		acknowledgeErr := acknowledgeProcessInitializationOutcome(ctx, c.acknowledger, failedProcessInitializationOutcome(admission, failure))
		return childStartJobResult{result: failedChildStart(
			c.spec, failure.Kind(), failure.Code(), errors.Join(err, acknowledgeErr),
		)}
	}
	if err := acknowledgeProcessInitializationOutcome(ctx, c.acknowledger, initializedProcessOutcome(admission, startedAt)); err != nil {
		return childStartJobResult{result: failedChildStart(
			c.spec, FailureKindExternal, childInitializationOutcomeUnacknowledgedCode, err,
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

func childSpecDigest(spec ChildSpec) (Digest, error) {
	payload, err := json.Marshal(spec)
	if err != nil {
		return Digest{}, err
	}
	return digestBytes(payload), nil
}

func deriveChildProcessID(effectID EffectID) ProcessID {
	digest := digestBytes([]byte("child\x00" + effectID.String()))
	id, err := ParseProcessID(processIDPrefix + digest.hex())
	if err != nil {
		panic(err)
	}
	return id
}
