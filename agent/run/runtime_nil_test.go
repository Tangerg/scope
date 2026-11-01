package run

import (
	startrand "crypto/rand"
	"errors"
	"reflect"
	"testing"
)

func TestNilEngineOperationsReportInvalidConfiguration(t *testing.T) {
	var engine *Runtime
	ctx := t.Context()
	operations := map[string]func() error{
		"Start": func() error {
			_, err := engine.Start(ctx, Program[Payload, Payload]{}, StartRequest[Payload]{ID: StartID{Scope: "test", Key: startrand.Text()}, Input: Payload{}})
			return err
		},
		"Run": func() error {
			_, err := observeOutcome(ctx, engine, Program[Payload, Payload]{}, Payload{})
			return err
		},
		"Close":             func() error { return engine.Close(ctx) },
		"FlushObservations": func() error { return engine.FlushObservations(ctx) },
		"InspectTree":       func() error { _, err := engine.InspectTree(ctx, ProcessID{}); return err },
		"ReleaseTree":       func() error { return engine.ReleaseTree(ctx, ProcessID{}) },
		"CaptureTree":       func() error { _, err := engine.CaptureTree(ctx, ProcessID{}); return err },
		"RestoreTree":       func() error { _, err := engine.Recover(ctx, Program[Payload, Payload]{}, TreeSnapshot{}); return err },
		"ValidateRestorableTree": func() error {
			return engine.ValidateRestorableTree(ctx, Program[Payload, Payload]{}, TreeSnapshot{})
		},
	}
	for name, operation := range operations {
		if err := operation(); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("nil Engine %s error = %v, want %v", name, err, ErrInvalidConfig)
		}
	}
	if _, found := engine.Process(ProcessID{}); found {
		t.Error("nil Engine found a Process")
	}
	if !reflect.DeepEqual(engine.ObservationFailures(), ObservationFailures{}) {
		t.Error("nil Engine reported observations")
	}
}

func TestEngineRejectsTypedNilCollaboratorsBeforeStartingObservation(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*Config)
	}{
		{"admitter", func(config *Config) { config.ProcessAdmitter = ProcessAdmitterFunc(nil) }},
		{"initialization acknowledger", func(config *Config) {
			config.ProcessInitializationAcknowledger = ProcessInitializationAcknowledgerFunc(nil)
		}},
		{"resolver", func(config *Config) { config.ProgramResolver = programResolverFunc(nil) }},
		{"event listener", func(config *Config) { config.EventListeners = []EventListener{EventListenerFunc(nil)} }},
		{"delta listener", func(config *Config) { config.DeltaListeners = []DeltaListener{DeltaListenerFunc(nil)} }},
		{"negative event capacity", func(config *Config) { config.EventBufferCapacity = -1 }},
		{"negative delta capacity", func(config *Config) { config.DeltaBufferCapacity = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := Config{Store: NewMemoryStore()}
			test.configure(&config)
			if engine, err := New(t.Context(), config); engine != nil || !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("unusable collaborator admitted: %v, %v", engine, err)
			}
		})
	}
}
