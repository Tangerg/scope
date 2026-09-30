package agent

import (
	"errors"
	"fmt"
	"slices"

	"github.com/samber/lo"
)

var ErrInvalidDeployment = errors.New("agent: invalid deployment")

// DeploymentConfig contains the complete behavior binding of one Deployment.
// The digests must cover the exact code artifact and all frozen dispatcher or
// Strategy configuration that can affect execution or restoration.
type DeploymentConfig struct {
	Definition Definition

	// Dispatcher interprets this Definition's external Effects. Nil binds a
	// Definition that uses only Framework Effects; a dispatcher-targeted Effect
	// then fails admission before any Effect in its Step can execute.
	Dispatcher Dispatcher

	ImplementationDigest Digest

	// ConfigurationDigest identifies all frozen behavior-affecting Definition
	// and Dispatcher configuration, including each finite or unlimited Strategy
	// quota. Quota JSON preserves that distinction for configuration hashing.
	// Child bindings are not restated here: NewDeployment folds the ones the
	// Definition reports through ChildDeployments into the DeploymentRef.
	ConfigurationDigest Digest
}

// Deployment is an immutable binding of one Definition, its optional external
// Dispatcher, and an exact value reference used by Process snapshots.
type Deployment struct {
	reference  DeploymentRef
	descriptor Descriptor
	definition Definition
	dispatcher Dispatcher
	children   []DeploymentRef
}

// NewDeployment freezes a Definition and Dispatcher under explicit
// implementation and configuration digests, so recovery resolves the exact
// behavior a snapshot names rather than whatever now shares its name.
func NewDeployment(config DeploymentConfig) (Deployment, error) {
	if lo.IsNil(config.Definition) {
		return Deployment{}, fmt.Errorf("%w: definition is required", ErrInvalidDeployment)
	}
	if config.Dispatcher != nil && lo.IsNil(config.Dispatcher) {
		return Deployment{}, fmt.Errorf("%w: dispatcher is typed nil", ErrInvalidDeployment)
	}
	descriptor, err := definitionDescriptor(config.Definition)
	if err != nil {
		return Deployment{}, err
	}
	children, bindings, err := definitionBindings(config.Definition)
	if err != nil {
		return Deployment{}, err
	}
	reference, err := newDeploymentRef(descriptor, config.ImplementationDigest, config.ConfigurationDigest, bindings)
	if err != nil {
		return Deployment{}, fmt.Errorf("%w: %w", ErrInvalidDeployment, err)
	}
	return Deployment{
		reference:  reference,
		descriptor: descriptor,
		definition: config.Definition,
		dispatcher: config.Dispatcher,
		children:   children,
	}, nil
}

func (d Deployment) DeploymentRef() DeploymentRef { return d.reference }

func (d Deployment) Descriptor() Descriptor { return d.descriptor }

func (d Deployment) Definition() Definition { return d.definition }

// ChildDeployments returns the child bindings folded into DeploymentRef, in
// digest order.
func (d Deployment) ChildDeployments() []DeploymentRef { return slices.Clone(d.children) }

// Valid checks the frozen binding without invoking user code. The Engine checks
// the live Definition contract at startup and restoration boundaries.
func (d Deployment) Valid() bool {
	return d.reference.Valid() && d.descriptor.Valid() &&
		!lo.IsNil(d.definition) && (d.dispatcher == nil || !lo.IsNil(d.dispatcher)) &&
		d.reference.ContractDigest() == d.descriptor.Digest()
}

func (d Deployment) validateDefinition() error {
	if !d.Valid() {
		return ErrInvalidDeployment
	}
	descriptor, err := definitionDescriptor(d.definition)
	if err != nil {
		return err
	}
	if descriptor.Digest() != d.descriptor.Digest() {
		return fmt.Errorf("%w: definition Descriptor differs from its frozen contract", ErrInvalidDeployment)
	}
	_, bindings, err := definitionBindings(d.definition)
	if err != nil {
		return err
	}
	if bindings != d.reference.BindingsDigest() {
		return fmt.Errorf("%w: definition child bindings differ from its frozen identity", ErrInvalidDeployment)
	}
	return nil
}

func definitionBindings(definition Definition) ([]DeploymentRef, Digest, error) {
	reported, err := invokeCallback("Definition.ChildDeployments", func() ([]DeploymentRef, error) {
		return definition.ChildDeployments(), nil
	})
	if err != nil {
		return nil, Digest{}, fmt.Errorf("%w: %w", ErrInvalidDeployment, err)
	}
	children, err := canonicalChildDeployments(reported)
	if err != nil {
		return nil, Digest{}, fmt.Errorf("%w: %w", ErrInvalidDeployment, err)
	}
	bindings, err := childBindingsDigest(children)
	if err != nil {
		return nil, Digest{}, fmt.Errorf("%w: bindings digest: %w", ErrInvalidDeployment, err)
	}
	return children, bindings, nil
}

func definitionDescriptor(definition Definition) (Descriptor, error) {
	descriptor, err := invokeCallback("Definition.Descriptor", func() (Descriptor, error) {
		return definition.Descriptor(), nil
	})
	if err != nil {
		return Descriptor{}, fmt.Errorf("%w: %w", ErrInvalidDeployment, err)
	}
	return descriptor, nil
}

func (d Deployment) validateEffect(effect Effect) error {
	if effect.Target() == EffectTargetDispatcher && d.dispatcher == nil {
		return fmt.Errorf("%w: dispatcher Effect requires a bound dispatcher", ErrInvalidEffect)
	}
	return nil
}
