package agent

import (
	"errors"
	"fmt"
	"slices"
	"strings"

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
	// children are the canonical static child bindings, ordered by
	// reference digest. Their references make up reference's bindings digest.
	children []Deployment
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
	reference, err := newDeploymentRef(deploymentIdentityWire{
		Name: descriptor.Name(), ContractDigest: descriptor.Digest(),
		ImplementationDigest: config.ImplementationDigest, ConfigurationDigest: config.ConfigurationDigest,
		BindingsDigest: bindings,
	})
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

// boundChild returns the static child binding with exactly reference.
func (d Deployment) boundChild(reference DeploymentRef) (Deployment, bool) {
	index, found := slices.BinarySearchFunc(d.children, reference, compareBoundChild)
	if !found {
		return Deployment{}, false
	}
	return d.children[index], true
}

// Valid checks the frozen binding without invoking user code. The Engine checks
// the live Definition contract at startup and restoration boundaries.
func (d Deployment) Valid() bool {
	return d.reference.Valid() && d.descriptor.Valid() &&
		!lo.IsNil(d.definition) && (d.dispatcher == nil || !lo.IsNil(d.dispatcher))
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

func definitionBindings(definition Definition) ([]Deployment, Digest, error) {
	reported, err := invokeCallback("Definition.ChildDeployments", func() ([]Deployment, error) {
		return definition.ChildDeployments(), nil
	})
	if err != nil {
		return nil, Digest{}, fmt.Errorf("%w: %w", ErrInvalidDeployment, err)
	}
	children := slices.Clone(reported)
	for _, child := range children {
		if !child.Valid() {
			return nil, Digest{}, fmt.Errorf("%w: child binding is invalid", ErrInvalidDeployment)
		}
	}
	slices.SortFunc(children, func(left, right Deployment) int {
		return compareBoundChild(left, right.reference)
	})
	children = slices.CompactFunc(children, func(left, right Deployment) bool { return left.reference == right.reference })
	references := make([]DeploymentRef, len(children))
	for index, child := range children {
		references[index] = child.reference
	}
	bindings, err := childBindingsDigest(references)
	if err != nil {
		return nil, Digest{}, fmt.Errorf("%w: bindings digest: %w", ErrInvalidDeployment, err)
	}
	return children, bindings, nil
}

// compareBoundChild orders static bindings by reference digest, so equal
// binding sets always produce the same bindings digest.
func compareBoundChild(child Deployment, reference DeploymentRef) int {
	return strings.Compare(child.reference.digest.String(), reference.digest.String())
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
