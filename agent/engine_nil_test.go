package agent

import (
	"errors"
	"testing"
)

func TestNilEngineOperationsReportInvalidConfiguration(t *testing.T) {
	var engine *Engine
	ctx := t.Context()
	operations := map[string]func() error{
		"Start":       func() error { _, err := engine.Start(ctx, Deployment{}, Payload{}); return err },
		"Run":         func() error { _, err := engine.Run(ctx, Deployment{}, Payload{}); return err },
		"Close":       func() error { return engine.Close(ctx) },
		"FlushDeltas": func() error { return engine.FlushDeltas(ctx) },
		"InspectTree": func() error { _, err := engine.InspectTree(ctx, ProcessID{}); return err },
		"ReleaseTree": func() error { return engine.ReleaseTree(ctx, ProcessID{}) },
		"CaptureTree": func() error { _, err := engine.CaptureTree(ctx, ProcessID{}); return err },
		"RestoreTree": func() error { _, err := engine.RestoreTree(ctx, Deployment{}, TreeSnapshot{}); return err },
		"ValidateRestorableTree": func() error {
			return engine.ValidateRestorableTree(ctx, Deployment{}, TreeSnapshot{})
		},
	}
	for name, operation := range operations {
		if err := operation(); !errors.Is(err, ErrInvalidEngineConfig) {
			t.Errorf("nil Engine %s error = %v, want %v", name, err, ErrInvalidEngineConfig)
		}
	}
	if _, found := engine.Process(ProcessID{}); found {
		t.Error("nil Engine found a Process")
	}
	if engine.ObservationFailures() != (ObservationFailures{}) {
		t.Error("nil Engine reported observations")
	}
}

func TestEngineRejectsTypedNilCollaboratorsBeforeStartingObservation(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*EngineConfig)
	}{
		{"admitter", func(config *EngineConfig) { config.ProcessAdmitter = ProcessAdmitterFunc(nil) }},
		{"initialization acknowledger", func(config *EngineConfig) {
			config.ProcessInitializationAcknowledger = ProcessInitializationAcknowledgerFunc(nil)
		}},
		{"resolver", func(config *EngineConfig) { config.DeploymentResolver = deploymentResolverFunc(nil) }},
		{"event listener", func(config *EngineConfig) { config.EventListeners = []EventListener{EventListenerFunc(nil)} }},
		{"delta listener", func(config *EngineConfig) { config.DeltaListeners = []DeltaListener{DeltaListenerFunc(nil)} }},
		{"negative delta capacity", func(config *EngineConfig) { config.DeltaBufferCapacity = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}
			test.configure(&config)
			if engine, err := NewEngine(config); engine != nil || !errors.Is(err, ErrInvalidEngineConfig) {
				t.Fatalf("unusable collaborator admitted: %v, %v", engine, err)
			}
		})
	}
}
