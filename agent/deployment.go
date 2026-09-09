package agent

import (
	"errors"
	"fmt"

	"github.com/samber/lo"
)

var ErrInvalidDeployment = errors.New("agent: invalid deployment")

// DeploymentConfig contains the complete behavior binding of one Deployment.
// The digests must cover the exact code artifact and all frozen dispatcher or
// Strategy configuration that can affect execution or restoration.
type DeploymentConfig struct {
	// Definition owns the Strategy contract and creates per-Process execution.
	Definition Definition

	// Dispatcher interprets this Definition's external Effects. Nil binds a
	// Definition that uses only Framework Effects; a dispatcher-targeted Effect
	// then fails admission before any Effect in its Step can execute.
	Dispatcher Dispatcher

	// ImplementationDigest identifies the exact executable Definition artifact.
	ImplementationDigest Digest

	// ConfigurationDigest identifies all frozen behavior-affecting Definition
	// and Dispatcher configuration.
	ConfigurationDigest Digest
}

// Deployment is an immutable binding of one Definition, its optional external
// Dispatcher, and an exact value reference used by Process snapshots.
type Deployment struct {
	reference  DeploymentRef
	descriptor Descriptor
	definition Definition
	dispatcher Dispatcher
}

// NewDeployment freezes a Definition and Dispatcher under explicit
// implementation and configuration digests. That binding lets recovery
// resolve the exact behavior a snapshot names instead of whatever now answers
// to the same Definition name.
func NewDeployment(config DeploymentConfig) (Deployment, error) {
	if lo.IsNil(config.Definition) {
		return Deployment{}, fmt.Errorf("%w: definition is required", ErrInvalidDeployment)
	}
	if config.Dispatcher != nil && lo.IsNil(config.Dispatcher) {
		return Deployment{}, fmt.Errorf("%w: dispatcher is typed nil", ErrInvalidDeployment)
	}
	descriptor := config.Definition.Descriptor()
	reference, err := newDeploymentRef(descriptor, config.ImplementationDigest, config.ConfigurationDigest)
	if err != nil {
		return Deployment{}, fmt.Errorf("%w: %w", ErrInvalidDeployment, err)
	}
	return Deployment{
		reference:  reference,
		descriptor: descriptor,
		definition: config.Definition,
		dispatcher: config.Dispatcher,
	}, nil
}

// DeploymentRef returns the exact value identity stored in Process snapshots.
func (d Deployment) DeploymentRef() DeploymentRef { return d.reference }

// Descriptor returns the frozen static Definition contract.
func (d Deployment) Descriptor() Descriptor { return d.descriptor }

// Definition returns the erased behavior definition bound to this Deployment.
func (d Deployment) Definition() Definition { return d.definition }

func (d Deployment) Valid() bool {
	return d.reference.Valid() && d.descriptor.Valid() &&
		!lo.IsNil(d.definition) && (d.dispatcher == nil || !lo.IsNil(d.dispatcher)) &&
		d.definition.Descriptor().Digest() == d.descriptor.Digest()
}

func (d Deployment) effectDispatcher() Dispatcher { return d.dispatcher }

func (d Deployment) validateEffect(effect Effect) error {
	if effect.Target() == EffectTargetDispatcher && d.dispatcher == nil {
		return fmt.Errorf("%w: dispatcher Effect requires a bound dispatcher", ErrInvalidEffect)
	}
	return nil
}
