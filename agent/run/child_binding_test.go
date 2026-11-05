package run

import (
	"errors"
	"testing"
)

func TestChildBindingFreezesAuthorityIntoAnAdmittedChildSpec(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	program := treeFixtureProgram(t, runtime, 0, false, false, Budget{})
	capabilities := controlValue(NewCapabilitySet(controlValue(ParseCapability("test.read"))))
	binding := ChildBinding{Program: program.Erase(), Budget: Budget{Steps: NewQuota(3)}, Capabilities: capabilities}
	key := controlValue(ParseChildKey("child"))
	input := controlValue(EncodePayload(1))
	spec := controlValue(binding.Spec(key, input))
	binding.Budget.Steps = NewQuota(9)
	binding.Capabilities = CapabilitySet{}
	if !spec.Valid() || spec.Key != key || spec.ProgramRef != program.ProgramRef() || spec.Budget.Steps != NewQuota(3) || !spec.Capabilities.Allows(capabilities) || !spec.Input.Equal(input) {
		t.Fatal("child specification retained mutable binding authority")
	}
	for _, test := range []struct {
		name    string
		binding ChildBinding
		key     ChildKey
		input   Payload
	}{
		{"missing binding", ChildBinding{}, key, input},
		{"missing identity", binding, ChildKey{}, input},
		{"missing input", binding, key, Payload{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.binding.Spec(test.key, test.input); !errors.Is(err, ErrInvalidChildStart) {
				t.Fatal(err)
			}
		})
	}
	if _, err := binding.Spec(key, controlValue(EncodePayload("wrong contract"))); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("child binding bypassed its declared input contract: %v", err)
	}
}
