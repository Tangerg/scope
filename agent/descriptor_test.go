package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
)

func TestDescriptorOwnsContractAndValidatesValues(t *testing.T) {
	inputSchema, err := SchemaFor[wireFixture]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := SchemaFor[wireFixture]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name:         "interaction.chat",
		Description:  "Runs one model and tool interaction.",
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !descriptor.Valid() || !descriptor.Digest().Valid() {
		t.Fatalf("descriptor is not valid: %+v", descriptor)
	}
	input, err := EncodePayload(wireFixture{Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if validateInputErr := descriptor.ValidateInput(input); validateInputErr != nil {
		t.Fatal(validateInputErr)
	}
	output, err := ParsePayload([]byte(`{"message":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := descriptor.ValidateOutput(output); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("ValidateOutput error = %v, want ErrInvalidPayload", err)
	}
}

func TestDescriptorDigestChangesWithContract(t *testing.T) {
	inputSchema, err := SchemaFor[wireFixture]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := SchemaFor[wireFixture]()
	if err != nil {
		t.Fatal(err)
	}
	base := DescriptorConfig{
		Name:         "interaction.chat",
		Description:  "Runs one model and tool interaction.",
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
	}
	first, err := NewDescriptor(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Description = "Runs one changed model and tool interaction."
	second, err := NewDescriptor(base)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() == second.Digest() {
		t.Fatal("descriptor digest did not change with description")
	}
	countSchema, err := SchemaFor[struct {
		Count int `json:"count"`
	}]()
	if err != nil {
		t.Fatal(err)
	}
	base.OutputSchema = countSchema
	third, err := NewDescriptor(base)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() == third.Digest() {
		t.Fatal("descriptor digest did not change with output schema")
	}
}

func TestDescriptorJSONRejectsDrift(t *testing.T) {
	descriptor := testDescriptor(t)
	data, err := jsonv2.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Descriptor
	if unmarshalErr := jsonv2.Unmarshal(data, &decoded); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if decoded.Digest() != descriptor.Digest() {
		t.Fatalf("decoded digest = %q, want %q", decoded.Digest(), descriptor.Digest())
	}

	var wire map[string]any
	if unmarshalErr := jsonv2.Unmarshal(data, &wire); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	wire["description"] = "Tampered descriptor."
	tampered, err := jsonv2.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonv2.Unmarshal(tampered, &decoded); !errors.Is(err, ErrInvalidDescriptor) {
		t.Fatalf("unmarshal tampered descriptor error = %v, want ErrInvalidDescriptor", err)
	}
}

func TestDescriptorRejectsInvalidIdentity(t *testing.T) {
	valid := descriptorConfig(t)
	for name, mutate := range map[string]func(*DescriptorConfig){
		"empty name":        func(config *DescriptorConfig) { config.Name = "" },
		"uppercase name":    func(config *DescriptorConfig) { config.Name = "Chat" },
		"empty description": func(config *DescriptorConfig) { config.Description = "" },
	} {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			if _, err := NewDescriptor(config); !errors.Is(err, ErrInvalidDescriptor) {
				t.Fatalf("NewDescriptor error = %v, want ErrInvalidDescriptor", err)
			}
		})
	}
}

func testDescriptor(t *testing.T) Descriptor {
	t.Helper()
	descriptor, err := NewDescriptor(descriptorConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func descriptorConfig(t *testing.T) DescriptorConfig {
	t.Helper()
	inputSchema, err := SchemaFor[wireFixture]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := SchemaFor[wireFixture]()
	if err != nil {
		t.Fatal(err)
	}
	return DescriptorConfig{
		Name:         "interaction.chat",
		Description:  "Runs one model and tool interaction.",
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
	}
}

func TestDescriptorDecodingEnforcesConstructionRules(t *testing.T) {
	descriptor := newEngineTestDefinition(t, "descriptor.valid", "complete").Descriptor()
	for _, description := range []string{" leading", "", "trailing "} {
		wire := descriptorWire{descriptorContractWire: descriptor.contractWire(), Digest: descriptor.Digest()}
		wire.Description = description
		data, err := jsonv2.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		decoded := descriptor
		if err := jsonv2.Unmarshal(data, &decoded); !errors.Is(err, ErrInvalidDescriptor) {
			t.Fatalf("decode description %q: %v", description, err)
		}
		if !decoded.Valid() || decoded.Digest() != descriptor.Digest() {
			t.Fatal("failed decoding changed the valid descriptor")
		}
	}
	if (Descriptor{}).Valid() {
		t.Fatal("zero Descriptor is valid")
	}
}

func TestDescriptorSignalContractIsFrozenAndRequiredOnWire(t *testing.T) {
	config := descriptorConfig(t)
	rejecting := controlValue(NewDescriptor(config))
	input := controlValue(EncodePayload(wireFixture{Message: "hello"}))
	if err := rejecting.ValidateSignal(input); !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("default Signal admission=%v", err)
	}
	config.SignalSchema = config.InputSchema
	accepting := controlValue(NewDescriptor(config))
	if accepting.Digest() == rejecting.Digest() {
		t.Fatal("signal policy is absent from descriptor identity")
	}
	if err := accepting.ValidateSignal(input); err != nil {
		t.Fatal(err)
	}
	if err := accepting.ValidateSignal(controlValue(EncodePayload(42))); !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("invalid Signal admission=%v", err)
	}
	data := controlValue(jsonv2.Marshal(accepting))
	var restored Descriptor
	if err := jsonv2.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Digest() != accepting.Digest() || restored.ValidateSignal(input) != nil {
		t.Fatal("signal contract changed on round trip")
	}
	var wire map[string]json.RawMessage
	if err := jsonv2.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	delete(wire, "signal_schema")
	if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(wire)), &restored); !errors.Is(err, ErrInvalidDescriptor) {
		t.Fatalf("missing signal contract accepted: %v", err)
	}
}
