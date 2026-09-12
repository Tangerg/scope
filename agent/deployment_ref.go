package agent

import (
	"encoding/json"
	"errors"
	"fmt"
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
	digest               Digest
}

func newDeploymentRef(descriptor Descriptor, implementationDigest, configurationDigest Digest) (DeploymentRef, error) {
	if !descriptor.Valid() {
		return DeploymentRef{}, fmt.Errorf("%w: %w", ErrInvalidDeploymentRef, ErrInvalidDescriptor)
	}
	if !implementationDigest.Valid() {
		return DeploymentRef{}, fmt.Errorf("%w: implementation: %w", ErrInvalidDeploymentRef, ErrInvalidDigest)
	}
	if !configurationDigest.Valid() {
		return DeploymentRef{}, fmt.Errorf("%w: configuration: %w", ErrInvalidDeploymentRef, ErrInvalidDigest)
	}
	reference := DeploymentRef{
		name:                 descriptor.Name(),
		contractDigest:       descriptor.Digest(),
		implementationDigest: implementationDigest,
		configurationDigest:  configurationDigest,
	}
	digest, err := reference.computeDigest()
	if err != nil {
		return DeploymentRef{}, fmt.Errorf("%w: digest: %w", ErrInvalidDeploymentRef, err)
	}
	reference.digest = digest
	return reference, nil
}

// Name returns the stable Definition name.
func (d DeploymentRef) Name() string { return d.name }

// ContractDigest returns the exact Descriptor contract identity.
func (d DeploymentRef) ContractDigest() Digest { return d.contractDigest }

// ImplementationDigest returns the exact executable implementation identity.
func (d DeploymentRef) ImplementationDigest() Digest { return d.implementationDigest }

// ConfigurationDigest returns the frozen behavior-affecting configuration
// identity, including dispatcher configuration.
func (d DeploymentRef) ConfigurationDigest() Digest { return d.configurationDigest }

// Digest returns the complete Deployment value identity.
func (d DeploymentRef) Digest() Digest { return d.digest }

func (d DeploymentRef) String() string {
	if !d.Valid() {
		return invalidDeploymentRefText
	}
	return d.name + "+" + d.digest.String()
}

func (d DeploymentRef) Valid() bool {
	if !validQualifiedName(d.name) || !d.contractDigest.Valid() || !d.implementationDigest.Valid() ||
		!d.configurationDigest.Valid() || !d.digest.Valid() {
		return false
	}
	want, err := d.computeDigest()
	return err == nil && want == d.digest
}

func (d DeploymentRef) MarshalJSON() ([]byte, error) {
	if !d.Valid() {
		return nil, ErrInvalidDeploymentRef
	}
	return json.Marshal(deploymentRefWire{
		deploymentIdentityWire: d.identityWire(),
		Digest:                 d.digest,
	})
}

func (d *DeploymentRef) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidDeploymentRef)
	}
	wire, err := wireJSON.decode[deploymentRefWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidDeploymentRef, err)
	}
	value := DeploymentRef{
		name:                 wire.Name,
		contractDigest:       wire.ContractDigest,
		implementationDigest: wire.ImplementationDigest,
		configurationDigest:  wire.ConfigurationDigest,
		digest:               wire.Digest,
	}
	if !value.Valid() {
		return fmt.Errorf("%w: digest or identity component does not match", ErrInvalidDeploymentRef)
	}
	*d = value
	return nil
}

type deploymentIdentityWire struct {
	Name                 string `json:"name"`
	ContractDigest       Digest `json:"contract_digest"`
	ImplementationDigest Digest `json:"implementation_digest"`
	ConfigurationDigest  Digest `json:"configuration_digest"`
}

type deploymentRefWire struct {
	deploymentIdentityWire
	Digest Digest `json:"digest"`
}

func (DeploymentRef) JSONSchemaAlias() any { return deploymentRefWire{} }

func (d DeploymentRef) identityWire() deploymentIdentityWire {
	return deploymentIdentityWire{
		Name:                 d.name,
		ContractDigest:       d.contractDigest,
		ImplementationDigest: d.implementationDigest,
		ConfigurationDigest:  d.configurationDigest,
	}
}

func (d DeploymentRef) computeDigest() (Digest, error) {
	data, err := json.Marshal(d.identityWire())
	if err != nil {
		return Digest{}, err
	}
	return digestBytes(data), nil
}
