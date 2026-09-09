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

// Relation returns the prospective Process identity and tree location.
func (p ProcessAdmission) Relation() ProcessRelation { return p.relation }

// DeploymentRef returns the exact prospective Deployment identity.
func (p ProcessAdmission) DeploymentRef() DeploymentRef { return p.deploymentRef }

// Descriptor returns the prospective Definition's static contract.
func (p ProcessAdmission) Descriptor() Descriptor { return p.descriptor }

// Budget returns the prospective Process's fixed non-renewable allocation.
func (p ProcessAdmission) Budget() Budget { return p.budget }

// Capabilities returns the prospective Process's immutable authority set.
func (p ProcessAdmission) Capabilities() CapabilitySet { return p.capabilities }

func (p ProcessAdmission) Valid() bool {
	return p.relation.Valid() && p.deploymentRef.Valid() &&
		p.descriptor.Valid() && p.budget.Valid() &&
		p.capabilities.Valid() &&
		p.deploymentRef.Name() == p.descriptor.Name() &&
		p.deploymentRef.ContractDigest() == p.descriptor.Digest()
}

// ProcessAdmitter decides whether one prospective root or child Process may
// initialize. Implementations may coordinate caller-owned external admission work,
// but must not create a Process, mutate the admission, or allocate Framework
// resources. A prepared Step may replay the same child admission with the same
// prospective Process identity after recovery.
// The runtime cancels an active child admission when its parent terminates;
// an accepted admission still receives its required initialization outcome.
//
// Implementations must respect ctx, return in bounded time, be safe for
// concurrent calls when shared, and must not re-enter the Engine or a Process.
// Framework identity is stable, but persistence, transactionality, charging,
// and business idempotency remain implementation responsibilities. Returning
// an error rejects only this prospective Process. Budget allocation, capability
// attenuation, and tree limits remain Engine invariants and cannot be changed
// by an admitter. Every accepted admission concludes with exactly one
// ProcessStartOutcome when an acknowledger is configured. Restore repeats
// neither admission nor its outcome for a captured Process.
type ProcessAdmitter interface {
	// Admit decides whether the immutable prospective Process may initialize.
	// Returning nil accepts only the supplied identity and resources; it cannot
	// enlarge Budget or Capabilities. Returning an error prevents initialization
	// and publication. Implementations honor ctx, are bounded and concurrency-
	// safe, and must tolerate the same prospective identity after recovery.
	Admit(ctx context.Context, admission ProcessAdmission) error
}

// ProcessAdmitterFunc adapts a plain function to the admitter interface, so a
// host quota or policy check does not require a named type.
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
				"%w: admitter panicked: %v",
				ErrProcessAdmissionRejected, recovered,
			)
		}
	}()
	if err := admitter.Admit(requireContext(ctx), admission); err != nil {
		return fmt.Errorf("%w: %w", ErrProcessAdmissionRejected, err)
	}
	return nil
}
