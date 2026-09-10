package agent

import (
	"context"
	"errors"
	"testing"
)

type descriptorBoundaryDefinition struct {
	Definition
	descriptor func() Descriptor
}

func (d *descriptorBoundaryDefinition) Descriptor() Descriptor { return d.descriptor() }

func TestDeploymentValidityUsesFrozenContract(t *testing.T) {
	for _, scenario := range []string{"changed descriptor", "panicking descriptor"} {
		t.Run(scenario, func(t *testing.T) {
			original := newEngineTestDefinition(t, "engine.wait", "wait")
			definition := &descriptorBoundaryDefinition{Definition: original, descriptor: original.Descriptor}
			deployment := engineTestDeployment(t, definition, nil)
			engine, err := NewEngine(EngineConfig{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { mustCloseEngine(t, engine) })
			input, err := EncodeInput(engineTestInput{Value: "frozen contract"})
			if err != nil {
				t.Fatal(err)
			}
			process, err := engine.Start(context.WithoutCancel(t.Context()), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if killErr := process.Kill(context.WithoutCancel(t.Context()), "test cleanup"); killErr != nil {
					t.Error(killErr)
				}
				_ = mustAwait(t, process)
			})
			waitForStatus(t, process, StatusWaiting)
			snapshot, err := engine.CaptureTree(t.Context(), process.ID())
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			definition.descriptor = func() Descriptor {
				calls++
				if scenario == "panicking descriptor" {
					panic("descriptor callback failed")
				}
				return Descriptor{}
			}
			if !deployment.Valid() || calls != 0 || deployment.Descriptor().Digest() != original.Descriptor().Digest() {
				t.Fatal("Valid did not inspect only the frozen binding")
			}
			if started, startErr := engine.Start(t.Context(), deployment, input); started != nil || !errors.Is(startErr, ErrInvalidDeployment) {
				t.Fatalf("Start accepted a changed Definition: %v, %v", started, startErr)
			}
			if restored, restoreErr := engine.RestoreTree(t.Context(), deployment, snapshot); restored != nil || !errors.Is(restoreErr, ErrInvalidDeployment) {
				t.Fatalf("RestoreTree accepted a changed Definition: %v, %v", restored, restoreErr)
			}
			if calls != 2 {
				t.Fatalf("Descriptor calls = %d, want one per execution boundary", calls)
			}
		})
	}
}

func TestNewDeploymentIsolatesDescriptorPanic(t *testing.T) {
	definition := &descriptorBoundaryDefinition{descriptor: func() Descriptor { panic("broken descriptor") }}
	deployment, err := NewDeployment(DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: ComputeDigest([]byte("descriptor boundary")),
		ConfigurationDigest:  ComputeDigest([]byte("descriptor boundary configuration")),
	})
	if deployment.Valid() || !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("NewDeployment = %v, %v, want ErrInvalidDeployment", deployment, err)
	}
}
