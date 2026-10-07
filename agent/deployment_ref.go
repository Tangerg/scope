package agent

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidDeploymentRef = errors.New("agent: invalid deployment reference")

const invalidDeploymentRefText = "<invalid-deployment-ref>"

// DeploymentRef is the immutable value identity of one exact Definition
// implementation and frozen execution configuration. It contains no registry
// pointer and is sufficient to reject restore against a different Deployment.
type DeploymentRef struct {
	name                 string
	contractDigest       Digest
	implementationDigest Digest
	configurationDigest  Digest
	bindingsDigest       Digest
	digest               Digest
}

// newDeploymentRef owns the identity rule for both a constructed Deployment
// and a decoded reference.
func newDeploymentRef(identity deploymentIdentityWire) (DeploymentRef, error) {
	if !ValidQualifiedName(identity.Name) {
		return DeploymentRef{}, fmt.Errorf("%w: name must be a qualified name", ErrInvalidDeploymentRef)
	}
	for _, component := range []struct {
		name   string
		digest Digest
	}{
		{"contract", identity.ContractDigest}, {"implementation", identity.ImplementationDigest},
		{"configuration", identity.ConfigurationDigest}, {"bindings", identity.BindingsDigest},
	} {
		if !component.digest.Valid() {
			return DeploymentRef{}, fmt.Errorf("%w: %s: %w", ErrInvalidDeploymentRef, component.name, ErrInvalidDigest)
		}
	}
	reference := DeploymentRef{
		name:                 identity.Name,
		contractDigest:       identity.ContractDigest,
		implementationDigest: identity.ImplementationDigest,
		configurationDigest:  identity.ConfigurationDigest,
		bindingsDigest:       identity.BindingsDigest,
	}
	digest, err := reference.computeDigest()
	if err != nil {
		return DeploymentRef{}, fmt.Errorf("%w: digest: %w", ErrInvalidDeploymentRef, err)
	}
	reference.digest = digest
	return reference, nil
}

func (d DeploymentRef) Name() string { return d.name }

func (d DeploymentRef) ContractDigest() Digest { return d.contractDigest }

func (d DeploymentRef) ImplementationDigest() Digest { return d.implementationDigest }

// ConfigurationDigest returns the frozen behavior-affecting configuration
// identity, including dispatcher configuration.
func (d DeploymentRef) ConfigurationDigest() Digest { return d.configurationDigest }

// BindingsDigest identifies the child Deployments the definition binds, as
// reported by Definition.ChildDeployments.
func (d DeploymentRef) BindingsDigest() Digest { return d.bindingsDigest }

func (d DeploymentRef) Digest() Digest { return d.digest }

func (d DeploymentRef) String() string {
	if !d.Valid() {
		return invalidDeploymentRefText
	}
	return d.name + "+" + d.digest.String()
}

func (d DeploymentRef) Valid() bool {
	return d.digest.Valid()
}

func (d DeploymentRef) MarshalJSON() ([]byte, error) {
	if !d.Valid() {
		return nil, ErrInvalidDeploymentRef
	}
	return jsonv2.Marshal(d.identityWire())
}

func (d *DeploymentRef) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidDeploymentRef)
	}
	wire, err := jsonwire.Decode[deploymentIdentityWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidDeploymentRef, err)
	}
	value, err := newDeploymentRef(wire)
	if err != nil {
		return err
	}
	*d = value
	return nil
}

// deploymentIdentityWire carries the components a DeploymentRef digest is
// computed from; the digest itself is never encoded.
type deploymentIdentityWire struct {
	Name                 string `json:"name"`
	ContractDigest       Digest `json:"contract_digest"`
	ImplementationDigest Digest `json:"implementation_digest"`
	ConfigurationDigest  Digest `json:"configuration_digest"`
	BindingsDigest       Digest `json:"bindings_digest"`
}

func (DeploymentRef) JSONSchemaAlias() any { return deploymentIdentityWire{} }

func (d DeploymentRef) identityWire() deploymentIdentityWire {
	return deploymentIdentityWire{
		Name:                 d.name,
		ContractDigest:       d.contractDigest,
		ImplementationDigest: d.implementationDigest,
		ConfigurationDigest:  d.configurationDigest,
		BindingsDigest:       d.bindingsDigest,
	}
}

func (d DeploymentRef) computeDigest() (Digest, error) {
	data, err := jsonv2.Marshal(d.identityWire())
	if err != nil {
		return Digest{}, err
	}
	return ComputeDigest(data), nil
}

func childBindingsDigest(canonical []DeploymentRef) (Digest, error) {
	digests := make([]Digest, len(canonical))
	for index, child := range canonical {
		digests[index] = child.digest
	}
	data, err := jsonv2.Marshal(digests)
	if err != nil {
		return Digest{}, err
	}
	return ComputeDigest(data), nil
}
