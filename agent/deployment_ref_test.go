package agent

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDeploymentRefBindsContractImplementationAndConfiguration(t *testing.T) {
	descriptor := testDescriptor(t)
	implementation := digestBytes([]byte("interaction implementation"))
	configuration := digestBytes([]byte("model and dispatcher configuration"))
	reference, err := newDeploymentRef(descriptor, implementation, configuration)
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

	changedImplementation, err := newDeploymentRef(descriptor, digestBytes([]byte("changed interaction implementation")), configuration)
	if err != nil {
		t.Fatal(err)
	}
	changedConfiguration, err := newDeploymentRef(descriptor, implementation, digestBytes([]byte("changed model and dispatcher configuration")))
	if err != nil {
		t.Fatal(err)
	}
	if reference.Digest() == changedImplementation.Digest() || reference.Digest() == changedConfiguration.Digest() {
		t.Fatal("Deployment digest did not change with exact implementation or configuration")
	}
}

func TestDeploymentRefStrictJSONRejectsTampering(t *testing.T) {
	reference, err := newDeploymentRef(testDescriptor(t), digestBytes([]byte("implementation")), digestBytes([]byte("configuration")))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DeploymentRef
	if unmarshalErr := json.Unmarshal(data, &decoded); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if decoded != reference {
		t.Fatalf("decoded DeploymentRef = %+v, want %+v", decoded, reference)
	}

	var wire map[string]any
	if unmarshalErr := json.Unmarshal(data, &wire); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	wire["name"] = "deployment.tampered"
	tampered, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(tampered, &decoded); !errors.Is(err, ErrInvalidDeploymentRef) {
		t.Fatalf("tampered DeploymentRef error = %v, want ErrInvalidDeploymentRef", err)
	}
}

func FuzzDeploymentRefJSONRoundTrip(f *testing.F) {
	reference, err := newDeploymentRef(testDescriptorForFuzz(f), ComputeDigest([]byte("implementation")), ComputeDigest([]byte("configuration")))
	if err != nil {
		f.Fatal(err)
	}
	seed, err := json.Marshal(reference)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Fuzz(func(t *testing.T, data []byte) {
		var decoded DeploymentRef
		if err := json.Unmarshal(data, &decoded); err != nil {
			return
		}
		encoded, err := json.Marshal(decoded)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip DeploymentRef
		if err := json.Unmarshal(encoded, &roundTrip); err != nil {
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
	reference, err := newDeploymentRef(testDescriptor(t), ComputeDigest([]byte("implementation")), ComputeDigest([]byte("configuration")))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "Invalid.Name", "invalid name"} {
		identity := reference.identityWire()
		identity.Name = name
		encodedIdentity, err := json.Marshal(identity)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(deploymentRefWire{deploymentIdentityWire: identity, Digest: ComputeDigest(encodedIdentity)})
		if err != nil {
			t.Fatal(err)
		}
		decoded := reference
		if err := json.Unmarshal(encoded, &decoded); !errors.Is(err, ErrInvalidDeploymentRef) {
			t.Fatalf("invalid identity %q accepted: %v", name, err)
		}
		if decoded != reference {
			t.Fatal("rejected identity changed the existing reference")
		}
	}
}
