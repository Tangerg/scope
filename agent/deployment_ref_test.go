package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
)

func TestDeploymentRefBindsContractImplementationAndConfiguration(t *testing.T) {
	descriptor := testDescriptor(t)
	implementation := digestBytes([]byte("interaction implementation"))
	configuration := digestBytes([]byte("model and dispatcher configuration"))
	reference, err := newDeploymentRef(descriptor, implementation, configuration, noChildBindings())
	if err != nil {
		t.Fatal(err)
	}
	if !reference.Valid() || reference.ContractDigest() != descriptor.Digest() || reference.ImplementationDigest() != implementation || reference.ConfigurationDigest() != configuration {
		t.Fatalf("DeploymentRef = %+v", reference)
	}
	wantText := descriptor.Name() + "+" + reference.Digest().String()
	if reference.String() != wantText || (DeploymentRef{}).String() != invalidDeploymentRefText {
		t.Fatalf("DeploymentRef text = %q, invalid = %q", reference.String(), (DeploymentRef{}).String())
	}

	changedImplementation, err := newDeploymentRef(descriptor, digestBytes([]byte("changed interaction implementation")), configuration, noChildBindings())
	if err != nil {
		t.Fatal(err)
	}
	changedConfiguration, err := newDeploymentRef(descriptor, implementation, digestBytes([]byte("changed model and dispatcher configuration")), noChildBindings())
	if err != nil {
		t.Fatal(err)
	}
	if reference.Digest() == changedImplementation.Digest() || reference.Digest() == changedConfiguration.Digest() {
		t.Fatal("Deployment digest did not change with exact implementation or configuration")
	}
}

func TestDeploymentRefStrictJSONRejectsTampering(t *testing.T) {
	reference, err := newDeploymentRef(testDescriptor(t), digestBytes([]byte("implementation")), digestBytes([]byte("configuration")), noChildBindings())
	if err != nil {
		t.Fatal(err)
	}
	data, err := jsonv2.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DeploymentRef
	if unmarshalErr := jsonv2.Unmarshal(data, &decoded); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if decoded != reference {
		t.Fatalf("decoded DeploymentRef = %+v, want %+v", decoded, reference)
	}

	var wire map[string]any
	if unmarshalErr := jsonv2.Unmarshal(data, &wire); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	wire["name"] = "deployment.tampered"
	tampered, err := jsonv2.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonv2.Unmarshal(tampered, &decoded); !errors.Is(err, ErrInvalidDeploymentRef) {
		t.Fatalf("tampered DeploymentRef error = %v, want ErrInvalidDeploymentRef", err)
	}
}

func FuzzDeploymentRefJSONRoundTrip(f *testing.F) {
	reference, err := newDeploymentRef(testDescriptorForFuzz(f), ComputeDigest([]byte("implementation")), ComputeDigest([]byte("configuration")), noChildBindings())
	if err != nil {
		f.Fatal(err)
	}
	seed, err := jsonv2.Marshal(reference)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Fuzz(func(t *testing.T, data []byte) {
		var decoded DeploymentRef
		if err := jsonv2.Unmarshal(data, &decoded); err != nil {
			return
		}
		encoded, err := jsonv2.Marshal(decoded)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip DeploymentRef
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
	schema, err := SchemaFor[wireFixture]()
	if err != nil {
		f.Fatal(err)
	}
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name:         "deployment.fuzz",
		Description:  "Validates DeploymentRef codec fuzz behavior.",
		InputSchema:  schema,
		OutputSchema: schema,
	})
	if err != nil {
		f.Fatal(err)
	}
	return descriptor
}

func TestDeploymentRefRejectsInvalidIdentityWithMatchingDigest(t *testing.T) {
	reference, err := newDeploymentRef(testDescriptor(t), ComputeDigest([]byte("implementation")), ComputeDigest([]byte("configuration")), noChildBindings())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "Invalid.Name", "invalid name"} {
		identity := reference.identityWire()
		identity.Name = name
		encodedIdentity, err := jsonv2.Marshal(identity)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := jsonv2.Marshal(deploymentRefWire{deploymentIdentityWire: identity, Digest: ComputeDigest(encodedIdentity)})
		if err != nil {
			t.Fatal(err)
		}
		decoded := reference
		if err := jsonv2.Unmarshal(encoded, &decoded); !errors.Is(err, ErrInvalidDeploymentRef) {
			t.Fatalf("invalid identity %q accepted: %v", name, err)
		}
		if decoded != reference {
			t.Fatal("rejected identity changed the existing reference")
		}
	}
}

// noChildBindings is the bindings digest of a leaf definition.
func noChildBindings() Digest { return controlValue(childBindingsDigest(nil)) }

func TestDeploymentRefDecodingRequiresItsBindingsDigest(t *testing.T) {
	reference := newChildTestDeployment(t).DeploymentRef()
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(reference)), &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["bindings_digest"]; !present {
		t.Fatal("encoded DeploymentRef omits its bindings digest")
	}
	delete(fields, "bindings_digest")
	var decoded DeploymentRef
	if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(fields)), &decoded); !errors.Is(err, ErrInvalidDeploymentRef) {
		t.Fatalf("DeploymentRef without bindings digest decoded: %v", err)
	}
}
