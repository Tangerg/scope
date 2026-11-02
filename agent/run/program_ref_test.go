package run

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestProgramRefBindsContractImplementationAndConfiguration(t *testing.T) {
	descriptor := testDescriptor(t)
	implementation := ComputeDigest([]byte("interaction implementation"))
	configuration := ComputeDigest([]byte("model and dispatcher configuration"))
	reference, err := descriptorProgramRef(descriptor, implementation, configuration, noChildBindings())
	if err != nil {
		t.Fatal(err)
	}
	if !reference.Valid() || reference.ContractDigest() != descriptor.Digest() || reference.ImplementationDigest() != implementation || reference.ConfigurationDigest() != configuration {
		t.Fatalf("ProgramRef = %+v", reference)
	}
	wantText := descriptor.Name() + "+" + reference.Digest().String()
	if reference.String() != wantText || (ProgramRef{}).String() != invalidProgramRefText {
		t.Fatalf("ProgramRef text = %q, invalid = %q", reference.String(), (ProgramRef{}).String())
	}

	changedImplementation, err := descriptorProgramRef(descriptor, ComputeDigest([]byte("changed interaction implementation")), configuration, noChildBindings())
	if err != nil {
		t.Fatal(err)
	}
	changedConfiguration, err := descriptorProgramRef(descriptor, implementation, ComputeDigest([]byte("changed model and dispatcher configuration")), noChildBindings())
	if err != nil {
		t.Fatal(err)
	}
	if reference.Digest() == changedImplementation.Digest() || reference.Digest() == changedConfiguration.Digest() {
		t.Fatal("Program digest did not change with exact implementation or configuration")
	}
}

func TestProgramRefDigestFollowsItsComponents(t *testing.T) {
	reference, err := descriptorProgramRef(testDescriptor(t), ComputeDigest([]byte("implementation")), ComputeDigest([]byte("configuration")), noChildBindings())
	if err != nil {
		t.Fatal(err)
	}
	data, err := jsonv2.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ProgramRef
	if unmarshalErr := jsonv2.Unmarshal(data, &decoded); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if decoded != reference {
		t.Fatalf("decoded ProgramRef = %+v, want %+v", decoded, reference)
	}

	var wire map[string]any
	if unmarshalErr := jsonv2.Unmarshal(data, &wire); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	wire["name"] = "deployment.changed"
	changed := controlValue(jsonv2.Marshal(wire))
	if err := jsonv2.Unmarshal(changed, &decoded); err != nil || decoded.Digest() == reference.Digest() {
		t.Fatalf("changed identity kept its digest: %v", err)
	}
	wire["name"], wire["digest"] = reference.Name(), reference.Digest().String()
	stored := controlValue(jsonv2.Marshal(wire))
	if err := jsonv2.Unmarshal(stored, &decoded); !errors.Is(err, ErrInvalidProgramRef) {
		t.Fatalf("stored ProgramRef digest error = %v, want ErrInvalidProgramRef", err)
	}
}

func FuzzProgramRefJSONRoundTrip(f *testing.F) {
	reference, err := descriptorProgramRef(testDescriptorForFuzz(f), ComputeDigest([]byte("implementation")), ComputeDigest([]byte("configuration")), noChildBindings())
	if err != nil {
		f.Fatal(err)
	}
	seed, err := jsonv2.Marshal(reference)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Fuzz(func(t *testing.T, data []byte) {
		var decoded ProgramRef
		if err := jsonv2.Unmarshal(data, &decoded); err != nil {
			return
		}
		encoded, err := jsonv2.Marshal(decoded)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip ProgramRef
		if err := jsonv2.Unmarshal(encoded, &roundTrip); err != nil {
			t.Fatal(err)
		}
		if roundTrip != decoded {
			t.Fatalf("round trip = %+v, want %+v", roundTrip, decoded)
		}
	})
}

func testDescriptorForFuzz(f *testing.F) Descriptor {
	f.Helper()
	schema, err := jsonschema.For[wireFixture]()
	if err != nil {
		f.Fatal(err)
	}
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name:         "deployment.fuzz",
		Description:  "Validates ProgramRef codec fuzz behavior.",
		InputSchema:  schema,
		OutputSchema: schema,
	})
	if err != nil {
		f.Fatal(err)
	}
	return descriptor
}

func TestProgramRefRejectsInvalidIdentity(t *testing.T) {
	reference, err := descriptorProgramRef(testDescriptor(t), ComputeDigest([]byte("implementation")), ComputeDigest([]byte("configuration")), noChildBindings())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "Invalid.Name", "invalid name"} {
		identity := reference.identityWire()
		identity.Name = name
		encoded, err := jsonv2.Marshal(identity)
		if err != nil {
			t.Fatal(err)
		}
		decoded := reference
		if err := jsonv2.Unmarshal(encoded, &decoded); !errors.Is(err, ErrInvalidProgramRef) {
			t.Fatalf("invalid identity %q accepted: %v", name, err)
		}
		if decoded != reference {
			t.Fatal("rejected identity changed the existing reference")
		}
	}
}

func noChildBindings() Digest { return controlValue(childBindingsDigest(nil)) }

func TestProgramRefDecodingRequiresItsBindingsDigest(t *testing.T) {
	reference := newChildTestDeployment(t).ProgramRef()
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(reference)), &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["bindings_digest"]; !present {
		t.Fatal("encoded ProgramRef omits its bindings digest")
	}
	delete(fields, "bindings_digest")
	var decoded ProgramRef
	if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(fields)), &decoded); !errors.Is(err, ErrInvalidProgramRef) {
		t.Fatalf("ProgramRef without bindings digest decoded: %v", err)
	}
}

func descriptorProgramRef(descriptor Descriptor, implementation, configuration, bindings Digest) (ProgramRef, error) {
	return newProgramRef(programIdentityWire{
		Name: descriptor.Name(), ContractDigest: descriptor.Digest(),
		ImplementationDigest: implementation, ConfigurationDigest: configuration, BindingsDigest: bindings,
	})
}
