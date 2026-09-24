package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// MaxDescriptionBytes bounds descriptions of advertised behavior.
const MaxDescriptionBytes = 4096

// ValidDescription checks descriptive text without repairing or reinterpreting it.
func ValidDescription(description string) bool {
	return description != "" && len(description) <= MaxDescriptionBytes && utf8.ValidString(description) && strings.TrimSpace(description) == description
}

var ErrInvalidDescriptor = errors.New("agent: invalid descriptor")

// DescriptorConfig contains the complete static contract of a Definition.
// Executable implementation and frozen configuration identity belong to a
// Deployment, not this contract.
type DescriptorConfig struct {
	// Name is a stable lowercase qualified Definition name.
	Name string

	// Description states the Definition's behavior for human and model-facing
	// discovery without execution-specific state.
	Description string

	// InputSchema is the authoritative structural contract for Process input.
	InputSchema Schema

	// OutputSchema is the authoritative structural contract for completed output.
	OutputSchema Schema

	// SignalSchema declares unaddressed Host input. The zero value rejects all
	// unaddressed Signals. Addressed replies belong to their external wait;
	// Engine-owned protocol frames never pass through this schema.
	SignalSchema Schema
}

func (d DescriptorConfig) validate() error {
	if !ValidQualifiedName(d.Name) {
		return fmt.Errorf("%w: name must start with a lowercase letter and contain only lowercase letters, digits, '.', '_' or '-'", ErrInvalidDescriptor)
	}
	if !ValidDescription(d.Description) {
		return fmt.Errorf("%w: description must be non-empty, trimmed UTF-8, and at most %d bytes", ErrInvalidDescriptor, MaxDescriptionBytes)
	}
	if !d.InputSchema.Valid() {
		return fmt.Errorf("%w: input schema: %w", ErrInvalidDescriptor, ErrInvalidSchema)
	}
	if !d.OutputSchema.Valid() {
		return fmt.Errorf("%w: output schema: %w", ErrInvalidDescriptor, ErrInvalidSchema)
	}
	return nil
}

// Descriptor is an immutable Definition contract. It contains no executable
// behavior or Deployment configuration.
type Descriptor struct {
	name         string
	description  string
	inputSchema  Schema
	outputSchema Schema
	signalSchema Schema
	digest       Digest
}

// NewDescriptor validates the schemas at construction because they enter the
// Deployment digest. A schema accepted here and rejected later would change a
// Deployment's identity after Processes had already been started against it.
func NewDescriptor(config DescriptorConfig) (Descriptor, error) {
	if err := config.validate(); err != nil {
		return Descriptor{}, err
	}
	if !config.SignalSchema.Valid() {
		var err error
		config.SignalSchema, err = ParseSchema([]byte("false"))
		if err != nil {
			return Descriptor{}, fmt.Errorf("%w: rejecting signal schema: %w", ErrInvalidDescriptor, err)
		}
	}
	descriptor := Descriptor{
		name:         config.Name,
		description:  config.Description,
		inputSchema:  config.InputSchema,
		outputSchema: config.OutputSchema,
		signalSchema: config.SignalSchema,
	}
	digest, err := descriptor.computeDigest()
	if err != nil {
		return Descriptor{}, fmt.Errorf("%w: digest: %w", ErrInvalidDescriptor, err)
	}
	descriptor.digest = digest
	return descriptor, nil
}

// Name returns the stable Definition name.
func (d Descriptor) Name() string { return d.name }

func (d Descriptor) Description() string { return d.description }

// InputSchema returns the immutable schema value.
func (d Descriptor) InputSchema() Schema { return d.inputSchema }

// OutputSchema returns the immutable schema value.
func (d Descriptor) OutputSchema() Schema { return d.outputSchema }

// SignalSchema returns the unaddressed input contract; false rejects all input.
func (d Descriptor) SignalSchema() Schema { return d.signalSchema }

func (d Descriptor) Digest() Digest { return d.digest }

func (d Descriptor) Valid() bool { return d.digest.Valid() }

func (d Descriptor) ValidateInput(input Payload) error {
	if !d.Valid() {
		return ErrInvalidDescriptor
	}
	if err := d.inputSchema.Validate(input.data); err != nil {
		return fmt.Errorf("%w: schema validation: %w", ErrInvalidPayload, err)
	}
	return nil
}

// ValidateSignal checks unaddressed Host input before mailbox admission.
func (d Descriptor) ValidateSignal(input Payload) error {
	if !d.Valid() {
		return ErrInvalidDescriptor
	}
	if err := d.signalSchema.Validate(input.data); err != nil {
		return fmt.Errorf("%w: unaddressed input: %w", ErrSignalRejected, err)
	}
	return nil
}

func (d Descriptor) ValidateOutput(output Payload) error {
	if !d.Valid() {
		return ErrInvalidDescriptor
	}
	if err := d.outputSchema.Validate(output.data); err != nil {
		return fmt.Errorf("%w: schema validation: %w", ErrInvalidPayload, err)
	}
	return nil
}

// EncodeInput converts value into a Payload and validates it against this
// Descriptor's authoritative input schema.
func (d Descriptor) EncodeInput[T any](value T) (Payload, error) {
	if !d.Valid() {
		return Payload{}, ErrInvalidDescriptor
	}
	input, err := EncodePayload(value)
	if err != nil {
		return Payload{}, err
	}
	if err := d.ValidateInput(input); err != nil {
		return Payload{}, err
	}
	return input, nil
}

// DecodeOutput validates output against this Descriptor's authoritative
// output schema and strictly decodes it into T.
func (d Descriptor) DecodeOutput[T any](output Payload) (T, error) {
	var zero T
	if !d.Valid() {
		return zero, ErrInvalidDescriptor
	}
	if err := d.ValidateOutput(output); err != nil {
		return zero, err
	}
	return output.Decode[T]()
}

func (d Descriptor) MarshalJSON() ([]byte, error) {
	if !d.Valid() {
		return nil, ErrInvalidDescriptor
	}
	return jsonv2.Marshal(descriptorWire{
		descriptorContractWire: d.contractWire(),
		Digest:                 d.digest,
	})
}

func (d *Descriptor) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidDescriptor)
	}
	wire, err := jsonwire.Decode[descriptorWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidDescriptor, err)
	}
	inputSchema, err := ParseSchema(wire.InputSchema)
	if err != nil {
		return fmt.Errorf("%w: input schema: %w", ErrInvalidDescriptor, err)
	}
	outputSchema, err := ParseSchema(wire.OutputSchema)
	if err != nil {
		return fmt.Errorf("%w: output schema: %w", ErrInvalidDescriptor, err)
	}
	signalSchema, err := ParseSchema(wire.SignalSchema)
	if err != nil {
		return fmt.Errorf("%w: signal schema: %w", ErrInvalidDescriptor, err)
	}
	value, err := NewDescriptor(DescriptorConfig{
		Name:         wire.Name,
		Description:  wire.Description,
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
		SignalSchema: signalSchema,
	})
	if err != nil {
		return err
	}
	if wire.Digest != value.digest {
		return fmt.Errorf("%w: digest does not match descriptor content", ErrInvalidDescriptor)
	}
	*d = value
	return nil
}

func (Descriptor) JSONSchemaAlias() any { return descriptorWire{} }

type descriptorContractWire struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
	SignalSchema json.RawMessage `json:"signal_schema"`
}

type descriptorWire struct {
	descriptorContractWire
	Digest Digest `json:"digest"`
}

func (d Descriptor) contractWire() descriptorContractWire {
	return descriptorContractWire{
		Name:         d.name,
		Description:  d.description,
		InputSchema:  d.inputSchema.JSON(),
		OutputSchema: d.outputSchema.JSON(),
		SignalSchema: d.signalSchema.JSON(),
	}
}

func (d Descriptor) computeDigest() (Digest, error) {
	data, err := jsonv2.Marshal(d.contractWire(), jsonv2.Deterministic(true))
	if err != nil {
		return Digest{}, err
	}
	return digestBytes(data), nil
}
