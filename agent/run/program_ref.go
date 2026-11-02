package run

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidProgramRef = errors.New("run: invalid program reference")

const invalidProgramRefText = "<invalid-program-ref>"

// ProgramRef is the immutable value identity of one exact Definition
// implementation and frozen execution configuration. It contains no registry
// pointer and is sufficient to reject restore against a different Program.
type ProgramRef struct {
	name                 string
	contractDigest       Digest
	implementationDigest Digest
	configurationDigest  Digest
	bindingsDigest       Digest
	digest               Digest
}

// newProgramRef owns the identity rule for both a constructed Program
// and a decoded reference.
func newProgramRef(identity programIdentityWire) (ProgramRef, error) {
	if !ValidQualifiedName(identity.Name) {
		return ProgramRef{}, fmt.Errorf("%w: name must be a qualified name", ErrInvalidProgramRef)
	}
	for _, component := range []struct {
		name   string
		digest Digest
	}{
		{"contract", identity.ContractDigest}, {"implementation", identity.ImplementationDigest},
		{"configuration", identity.ConfigurationDigest}, {"bindings", identity.BindingsDigest},
	} {
		if !component.digest.Valid() {
			return ProgramRef{}, fmt.Errorf("%w: %s: %w", ErrInvalidProgramRef, component.name, ErrInvalidDigest)
		}
	}
	reference := ProgramRef{
		name:                 identity.Name,
		contractDigest:       identity.ContractDigest,
		implementationDigest: identity.ImplementationDigest,
		configurationDigest:  identity.ConfigurationDigest,
		bindingsDigest:       identity.BindingsDigest,
	}
	digest, err := reference.computeDigest()
	if err != nil {
		return ProgramRef{}, fmt.Errorf("%w: digest: %w", ErrInvalidProgramRef, err)
	}
	reference.digest = digest
	return reference, nil
}

func (p ProgramRef) Name() string { return p.name }

func (p ProgramRef) ContractDigest() Digest { return p.contractDigest }

func (p ProgramRef) ImplementationDigest() Digest { return p.implementationDigest }

// ConfigurationDigest returns the frozen behavior-affecting configuration
// identity, including dispatcher configuration.
func (p ProgramRef) ConfigurationDigest() Digest { return p.configurationDigest }

// BindingsDigest identifies the child Programs the definition binds, as
// reported by Definition.ChildPrograms.
func (p ProgramRef) BindingsDigest() Digest { return p.bindingsDigest }

func (p ProgramRef) Digest() Digest { return p.digest }

func (p ProgramRef) String() string {
	if !p.Valid() {
		return invalidProgramRefText
	}
	return p.name + "+" + p.digest.String()
}

func (p ProgramRef) Valid() bool {
	return p.digest.Valid()
}

func (p ProgramRef) MarshalJSON() ([]byte, error) {
	if !p.Valid() {
		return nil, ErrInvalidProgramRef
	}
	return jsonv2.Marshal(p.identityWire())
}

func (p *ProgramRef) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidProgramRef)
	}
	wire, err := jsonwire.Decode[programIdentityWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidProgramRef, err)
	}
	value, err := newProgramRef(wire)
	if err != nil {
		return err
	}
	*p = value
	return nil
}

// programIdentityWire carries the components a ProgramRef digest is
// computed from; the digest itself is never encoded.
type programIdentityWire struct {
	Name                 string `json:"name"`
	ContractDigest       Digest `json:"contract_digest"`
	ImplementationDigest Digest `json:"implementation_digest"`
	ConfigurationDigest  Digest `json:"configuration_digest"`
	BindingsDigest       Digest `json:"bindings_digest"`
}

func (ProgramRef) JSONSchemaModel() any { return programIdentityWire{} }

func (p ProgramRef) identityWire() programIdentityWire {
	return programIdentityWire{
		Name:                 p.name,
		ContractDigest:       p.contractDigest,
		ImplementationDigest: p.implementationDigest,
		ConfigurationDigest:  p.configurationDigest,
		BindingsDigest:       p.bindingsDigest,
	}
}

func (p ProgramRef) computeDigest() (Digest, error) {
	data, err := jsonv2.Marshal(p.identityWire())
	if err != nil {
		return Digest{}, err
	}
	return ComputeDigest(data), nil
}

func childBindingsDigest(canonical []ProgramRef) (Digest, error) {
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
