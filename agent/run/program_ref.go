package run

import (
	jsonv2 "encoding/json/v2"
	"errors"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidProgramRef = errors.New("run: invalid program reference")

type Compiler struct{ build string }

type ProgramRef struct {
	name          string
	build         string
	contract      Digest
	configuration Digest
	revision      string
	children      Digest
	digest        Digest
}

func newProgramRef(wire programIdentityWire) (ProgramRef, error) {
	if !ValidQualifiedName(wire.Name) || !ValidDescription(wire.Build) || wire.Revision != "" && !ValidDescription(wire.Revision) || !wire.Contract.Valid() || !wire.Configuration.Valid() || !wire.Children.Valid() {
		return ProgramRef{}, ErrInvalidProgramRef
	}
	encoded, err := jsonv2.Marshal(wire)
	if err != nil {
		return ProgramRef{}, err
	}
	return ProgramRef{name: wire.Name, build: wire.Build, contract: wire.Contract, configuration: wire.Configuration, revision: wire.Revision, children: wire.Children, digest: ComputeDigest(encoded)}, nil
}

func (p ProgramRef) Name() string   { return p.name }
func (p ProgramRef) Build() string  { return p.build }
func (p ProgramRef) Digest() Digest { return p.digest }
func (p ProgramRef) Valid() bool    { return p.digest.Valid() }
func (p ProgramRef) String() string {
	if !p.Valid() {
		return "<invalid-program-ref>"
	}
	return p.name + "+" + p.digest.String()
}

func (p ProgramRef) MarshalJSON() ([]byte, error) {
	if !p.Valid() {
		return nil, ErrInvalidProgramRef
	}
	return jsonv2.Marshal(p.wire())
}

func (p *ProgramRef) UnmarshalJSON(data []byte) error {
	if p == nil {
		return ErrInvalidProgramRef
	}
	wire, err := jsonwire.Decode[programIdentityWire](data)
	if err != nil {
		return err
	}
	value, err := newProgramRef(wire)
	if err == nil {
		*p = value
	}
	return err
}

func (p ProgramRef) wire() programIdentityWire {
	return programIdentityWire{Name: p.name, Build: p.build, Contract: p.contract, Configuration: p.configuration, Revision: p.revision, Children: p.children}
}

func (ProgramRef) JSONSchemaModel() any { return programIdentityWire{} }

type programIdentityWire struct {
	Name          string `json:"name"`
	Build         string `json:"build"`
	Contract      Digest `json:"contract"`
	Configuration Digest `json:"configuration"`
	Revision      string `json:"revision" jsonwire:"required"`
	Children      Digest `json:"children"`
}

func childBindingsDigest(children []ProgramRef) (Digest, error) {
	encoded, err := jsonv2.Marshal(children)
	if err != nil {
		return Digest{}, err
	}
	return ComputeDigest(encoded), nil
}
