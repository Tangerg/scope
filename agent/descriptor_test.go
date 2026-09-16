package agent

import (
	"encoding/json"
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
	data, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Descriptor
	if unmarshalErr := json.Unmarshal(data, &decoded); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if decoded.Digest() != descriptor.Digest() {
		t.Fatalf("decoded digest = %q, want %q", decoded.Digest(), descriptor.Digest())
	}

	var wire map[string]any
	if unmarshalErr := json.Unmarshal(data, &wire); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	wire["description"] = "Tampered descriptor."
	tampered, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(tampered, &decoded); !errors.Is(err, ErrInvalidDescriptor) {
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

func TestDescriptorValidityMatchesConstructionRules(t *testing.T) {
	descriptor := newEngineTestDefinition(t, "descriptor.valid", "complete").Descriptor()
	for _, malformed := range []Descriptor{
		{name: "Bad Name", description: descriptor.description, inputSchema: descriptor.inputSchema, outputSchema: descriptor.outputSchema, digest: descriptor.digest},
		{name: descriptor.name, description: " leading", inputSchema: descriptor.inputSchema, outputSchema: descriptor.outputSchema, digest: descriptor.digest},
		{name: descriptor.name, inputSchema: descriptor.inputSchema, outputSchema: descriptor.outputSchema, digest: descriptor.digest},
	} {
		if malformed.Valid() {
			t.Fatal("descriptor accepted content its constructor rejects")
		}
	}
}
