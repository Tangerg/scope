package run

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
	"github.com/Tangerg/scope/core/jsonschema"
)

// rejectEverySchema is the JSON Schema no value satisfies; a Definition that
// declares no Signal schema accepts no unaddressed input.
const rejectEverySchema = "false"

const MaxDescriptionBytes = 4096

func ValidDescription(description string) bool {
	return validBoundedText(description, MaxDescriptionBytes)
}

var ErrInvalidDescriptor = errors.New("run: invalid descriptor")

// DescriptorConfig contains the complete static contract of a Definition.
// Executable implementation and frozen configuration identity belong to a
// Program, not this contract.
type DescriptorConfig struct {
	Name string

	Description string

	InputSchema jsonschema.Schema

	OutputSchema jsonschema.Schema

	// SignalSchema declares unaddressed Host input. The zero value rejects all
	// unaddressed Signals. Addressed replies belong to their external wait;
	// Runtime-owned protocol frames never pass through this schema.
	SignalSchema jsonschema.Schema
}

func (d DescriptorConfig) validate() error {
	if !ValidQualifiedName(d.Name) {
		return fmt.Errorf("%w: name must start with a lowercase letter and contain only lowercase letters, digits, '.', '_' or '-'", ErrInvalidDescriptor)
	}
	if !ValidDescription(d.Description) {
		return fmt.Errorf("%w: description must be non-empty, trimmed UTF-8, and at most %d bytes", ErrInvalidDescriptor, MaxDescriptionBytes)
	}
	if !d.InputSchema.Valid() {
		return fmt.Errorf("%w: input schema: %w", ErrInvalidDescriptor, jsonschema.ErrInvalid)
	}
	if !d.OutputSchema.Valid() {
		return fmt.Errorf("%w: output schema: %w", ErrInvalidDescriptor, jsonschema.ErrInvalid)
	}
	return nil
}

// Descriptor is an immutable Definition contract. It contains no executable
// behavior or Program configuration.
type Descriptor struct {
	name         string
	description  string
	inputSchema  jsonschema.Schema
	outputSchema jsonschema.Schema
	signalSchema jsonschema.Schema
	digest       Digest
}

func NewDescriptor(config DescriptorConfig) (Descriptor, error) {
	if err := config.validate(); err != nil {
		return Descriptor{}, err
	}
	if !config.SignalSchema.Valid() {
		var err error
		config.SignalSchema, err = jsonschema.Parse([]byte(rejectEverySchema))
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

func (d Descriptor) Name() string { return d.name }

func (d Descriptor) Description() string { return d.description }

func (d Descriptor) InputSchema() jsonschema.Schema { return d.inputSchema }

func (d Descriptor) OutputSchema() jsonschema.Schema { return d.outputSchema }

// SignalSchema returns the unaddressed input contract; false rejects all input.
func (d Descriptor) SignalSchema() jsonschema.Schema { return d.signalSchema }

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
	return jsonv2.Marshal(d.contractWire())
}

func (d *Descriptor) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidDescriptor)
	}
	wire, err := jsonwire.Decode[descriptorContractWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidDescriptor, err)
	}
	inputSchema, err := jsonschema.Parse(wire.InputSchema)
	if err != nil {
		return fmt.Errorf("%w: input schema: %w", ErrInvalidDescriptor, err)
	}
	outputSchema, err := jsonschema.Parse(wire.OutputSchema)
	if err != nil {
		return fmt.Errorf("%w: output schema: %w", ErrInvalidDescriptor, err)
	}
	signalSchema, err := jsonschema.Parse(wire.SignalSchema)
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
	*d = value
	return nil
}

func (Descriptor) JSONSchemaModel() any { return descriptorContractWire{} }

// descriptorContractWire carries the contract a Descriptor digest is computed
// from; the digest itself is never encoded.
type descriptorContractWire struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
	SignalSchema json.RawMessage `json:"signal_schema"`
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
	return ComputeDigest(data), nil
}
