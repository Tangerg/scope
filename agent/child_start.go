package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	childRequestInvalidCode             = "engine.child.request.invalid"
	childIdentityConflictCode           = "engine.child.identity_conflict"
	childCapabilityEscalationCode       = "engine.child.capability_escalation"
	childBudgetExhaustedCode            = "engine.child.budget_exhausted"
	childBudgetInvalidCode              = "engine.child.budget_invalid"
	childTreeLimitCode                  = "engine.child.tree_limit"
	childStartUnavailableCode           = "engine.child.start.unavailable"
	childStartInterruptedCode           = "engine.child.start.interrupted"
	childDeploymentUnavailableCode      = "engine.child.deployment_unavailable"
	childInputInvalidCode               = "engine.child.input.invalid"
	childAdmissionRejectedCode          = "engine.child.admission.rejected"
	childStartOutcomeUnacknowledgedCode = "engine.child.start_outcome.unacknowledged"
	childSettlementInvalidCode          = "engine.child.settlement.invalid"
)

type childStartPreparation struct {
	plan   *childStartPlan
	result ChildStartResult
}

type childStartPlan struct {
	admitter         ProcessAdmitter
	acknowledger     ProcessStartOutcomeAcknowledger
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

func (t *treeRuntime) prepareChildStart(
	process *processState,
	effectID EffectID,
	spec ChildSpec,
) childStartPreparation {
	if !spec.Valid() || !process.handle.relation.Valid() {
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindContract, childRequestInvalidCode, ErrInvalidChildStart,
		)}
	}
	childID := deriveChildProcessID(effectID)
	relation := childProcessRelation(childID, process.handle.relation, spec.Key)
	requestDigest, err := childSpecDigest(spec)
	if err != nil {
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindContract, childRequestInvalidCode, err,
		)}
	}
	if existing, exists := t.engine.Process(childID); exists {
		if existing.Relation() == relation && existing.DeploymentRef() == spec.DeploymentRef &&
			existing.handle.childRequestDigest == requestDigest {
			return childStartPreparation{result: ChildStartResult{
				key: spec.Key, processID: childID, deploymentRef: spec.DeploymentRef,
			}}
		}
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindContract, childIdentityConflictCode, ErrInvalidChildStart,
		)}
	}
	if !process.capabilities.Allows(spec.Capabilities) {
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindContract, childCapabilityEscalationCode, ErrInvalidCapability,
		)}
	}
	if !process.reserveProvisionalChildBudget(spec.Budget) {
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindExecution, childBudgetExhaustedCode, ErrResourceLimitExceeded,
		)}
	}
	childLimits, err := limitsFromBudget(process.limits, spec.Budget)
	if err != nil {
		process.releaseProvisionalChildBudget(spec.Budget)
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindExecution, childBudgetInvalidCode, err,
		)}
	}
	if reserveProcessStartErr := t.engine.reserveProcessStart(
		relation, spec.DeploymentRef, process.treeLimits, requestDigest,
	); reserveProcessStartErr != nil {
		process.releaseProvisionalChildBudget(spec.Budget)
		if errors.Is(reserveProcessStartErr, ErrResourceLimitExceeded) {
			return childStartPreparation{result: failedChildStart(
				spec, FailureKindExecution, childTreeLimitCode, reserveProcessStartErr,
			)}
		}
		if errors.Is(reserveProcessStartErr, ErrEngineClosed) {
			return childStartPreparation{result: failedChildStart(
				spec, FailureKindExternal, childStartUnavailableCode, reserveProcessStartErr,
			)}
		}
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindContract, childIdentityConflictCode, reserveProcessStartErr,
		)}
	}
	return childStartPreparation{plan: &childStartPlan{
		admitter: t.engine.admitter, acknowledger: t.engine.startOutcomeAcknowledger,
		resolver: t.engine.resolver, parentDeployment: process.deployment,
		spec: spec, childID: childID, relation: relation,
		limits: childLimits, treeLimits: process.treeLimits,
		requestDigest: requestDigest,
	}}
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
		acknowledgeErr := acknowledgeProcessStartOutcome(ctx, c.acknowledger, abortedProcessOutcome(admission, failure))
		return childStartJobResult{result: failedChildStart(
			c.spec, failure.Kind(), failure.Code(), errors.Join(err, acknowledgeErr),
		)}
	}
	if err := acknowledgeProcessStartOutcome(ctx, c.acknowledger, startedProcessOutcome(admission, startedAt)); err != nil {
		return childStartJobResult{result: failedChildStart(
			c.spec, FailureKindExternal, childStartOutcomeUnacknowledgedCode, err,
		)}
	}
	return childStartJobResult{
		result: ChildStartResult{
			key: c.spec.Key, processID: c.childID, deploymentRef: c.spec.DeploymentRef,
		},
		deployment: deployment, execution: execution, state: state, startedAt: startedAt,
	}
}

func (p *processState) reserveProvisionalChildBudget(requested Budget) bool {
	if p.provisionalChildBudget.Valid() {
		return false
	}
	reserved, ok := p.reservedBudget.add(Budget{
		Steps: 1, Signals: uint64(len(p.prepared.wire.Effects)),
	})
	if !ok || !p.budget.canAllocate(p.usage, reserved, requested) {
		return false
	}
	p.provisionalChildBudget = requested
	return true
}

func (p *processState) commitProvisionalChildBudget(requested Budget) error {
	if !requested.Valid() || p.provisionalChildBudget != requested {
		return ErrResourceLimitExceeded
	}
	reserved, ok := p.reservedBudget.add(requested)
	if !ok {
		return ErrResourceLimitExceeded
	}
	p.reservedBudget = reserved
	p.provisionalChildBudget = Budget{}
	return nil
}

func (p *processState) releaseProvisionalChildBudget(requested Budget) {
	if p.provisionalChildBudget == requested {
		p.provisionalChildBudget = Budget{}
	}
}

func (p *processState) releaseCommittedChildBudget(released Budget) {
	if released.Steps > p.reservedBudget.Steps ||
		released.Effects > p.reservedBudget.Effects ||
		released.Signals > p.reservedBudget.Signals {
		return
	}
	p.reservedBudget.Steps -= released.Steps
	p.reservedBudget.Effects -= released.Effects
	p.reservedBudget.Signals -= released.Signals
}

func (p *processState) effectiveReservedBudget() Budget {
	reserved, ok := p.reservedBudget.add(p.provisionalChildBudget)
	if !ok {
		panic("agent: Process resource reservation overflow")
	}
	return reserved
}

func (c *childStartPlan) resolveDeployment() (Deployment, error) {
	reference := c.spec.DeploymentRef
	if reference == c.parentDeployment.DeploymentRef() {
		return c.parentDeployment, nil
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
	return resolver.Resolve(reference)
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
