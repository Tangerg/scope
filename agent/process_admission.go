package agent

import (
	"context"
	"errors"
	"fmt"
)

// ErrProcessAdmissionRejected marks failure at the policy boundary before execution starts.
var ErrProcessAdmissionRejected = errors.New("agent: process admission rejected")

// ProcessAdmission is the immutable Framework-owned information supplied to a
// ProcessAdmitter immediately before one root or child Process starts. It does
// not expose Input, Execution, Dispatcher, product identity, or Host state.
type ProcessAdmission struct {
	relation      ProcessRelation
	deploymentRef DeploymentRef
	descriptor    Descriptor
	budget        Budget
	capabilities  CapabilitySet
}

func (p ProcessAdmission) Relation() ProcessRelation { return p.relation }

func (p ProcessAdmission) DeploymentRef() DeploymentRef { return p.deploymentRef }

func (p ProcessAdmission) Descriptor() Descriptor { return p.descriptor }

func (p ProcessAdmission) Budget() Budget { return p.budget }

// Capabilities returns the prospective Process's immutable authority set.
func (p ProcessAdmission) Capabilities() CapabilitySet { return p.capabilities }

func (p ProcessAdmission) Valid() bool {
	return p.relation.Valid() && p.deploymentRef.Valid() &&
		p.descriptor.Valid() &&
		p.capabilities.Valid() &&
		p.deploymentRef.Name() == p.descriptor.Name() &&
		p.deploymentRef.ContractDigest() == p.descriptor.Digest()
}

// ProcessAdmitter decides whether one prospective root or child Process may
// initialize. Implementations may coordinate caller-owned external admission
// work but must not create a Process, mutate the admission, allocate Framework
// resources, or re-enter the Engine. They must respect ctx, return in bounded
// time, and be safe for concurrent calls. Persistence, charging, and business
// idempotency are implementation responsibilities; a prepared Step may replay
// the same child admission with the same prospective identity after recovery.
//
// The runtime cancels an active child admission when its parent terminates.
// Every accepted admission concludes with exactly one
// ProcessInitializationOutcome when an acknowledger is configured; a child that
// initializes anyway is published and then terminated with its parent. Restore
// repeats neither admission nor its outcome for a captured Process.
type ProcessAdmitter interface {
	// Admit returns nil to accept only the supplied identity and resources, or
	// an error to reject this prospective Process before initialization. It
	// cannot enlarge Budget or Capabilities.
	Admit(ctx context.Context, admission ProcessAdmission) error
}

type ProcessAdmitterFunc func(ctx context.Context, admission ProcessAdmission) error

func (p ProcessAdmitterFunc) Admit(ctx context.Context, admission ProcessAdmission) error {
	return p(ctx, admission)
}

func newProcessAdmission(
	relation ProcessRelation,
	deployment Deployment,
	budget Budget,
	capabilities CapabilitySet,
) ProcessAdmission {
	return ProcessAdmission{
		relation: relation, deploymentRef: deployment.DeploymentRef(),
		descriptor: deployment.Descriptor(), budget: budget,
		capabilities: capabilities,
	}
}

func requestProcessAdmission(
	ctx context.Context,
	admitter ProcessAdmitter,
	admission ProcessAdmission,
) (err error) {
	if admitter == nil {
		return nil
	}
	if !admission.Valid() {
		return fmt.Errorf("%w: invalid admission", ErrProcessAdmissionRejected)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf(
				"%w: %w",
				ErrProcessAdmissionRejected, callbackPanic("ProcessAdmitter.Admit", recovered),
			)
		}
	}()
	defer func() { err = sealCallbackError(err) }()
	if err := admitter.Admit(RequireContext(ctx), admission); err != nil {
		return fmt.Errorf("%w: %w", ErrProcessAdmissionRejected, err)
	}
	return nil
}
