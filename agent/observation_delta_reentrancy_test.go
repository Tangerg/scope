package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestDeltaListenerCannotJoinItsOwnDelivery(t *testing.T) {
	for _, test := range []struct {
		name    string
		call    func(*Engine, context.Context) error
		closing bool
	}{
		{"flush while open", (*Engine).FlushDeltas, false},
		{"close while open", (*Engine).Close, false},
		{"flush while closing", (*Engine).FlushDeltas, true},
		{"close while closing", (*Engine).Close, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered, proceed := make(chan struct{}), make(chan struct{})
				observed := make(chan error, 1)
				engine := runDeltaListenerProcess(t, func(ctx context.Context, engine *Engine, _ Delta) {
					close(entered)
					<-proceed
					ctx, cancel := context.WithTimeout(ctx, time.Second)
					defer cancel()
					observed <- test.call(engine, ctx)
				})
				<-entered
				closed := make(chan error, 1)
				if test.closing {
					go func() { closed <- engine.Close(t.Context()) }()
					synctest.Wait()
				}
				close(proceed)
				if err := <-observed; !errors.Is(err, ErrListenerReentrancy) {
					t.Errorf("Delta listener operation = %v, want ErrListenerReentrancy", err)
				}
				if test.closing {
					if err := <-closed; err != nil {
						t.Errorf("external Close = %v", err)
					}
				}
			})
		})
	}
}

func TestDeltaListenerContextExpiresAfterReturnOrPanic(t *testing.T) {
	for _, test := range []struct {
		name   string
		panics uint64
	}{
		{"return", 0},
		{"panic", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			contexts := make(chan context.Context, 1)
			engine := runDeltaListenerProcess(t, func(ctx context.Context, _ *Engine, _ Delta) {
				contexts <- ctx
				if test.panics != 0 {
					panic("Delta listener failed")
				}
			})
			ctx := <-contexts
			if err := engine.FlushDeltas(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := engine.ObservationFailures().DeltaListenerPanics(); got != test.panics {
				t.Fatalf("listener panics = %d, want %d", got, test.panics)
			}
			if err := engine.FlushDeltas(ctx); err != nil {
				t.Fatalf("FlushDeltas after callback returned = %v", err)
			}
			if err := engine.Close(ctx); err != nil {
				t.Fatalf("Close after callback returned = %v", err)
			}
		})
	}
}

func TestDeltaListenerCanJoinAnotherEngine(t *testing.T) {
	other, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, other) })
	observed := make(chan error, 1)
	engine := runDeltaListenerProcess(t, func(ctx context.Context, _ *Engine, _ Delta) {
		observed <- errors.Join(other.FlushDeltas(ctx), other.Close(ctx))
	})
	if err := engine.FlushDeltas(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-observed; err != nil {
		t.Fatalf("joining independent Engine = %v", err)
	}
}

func runDeltaListenerProcess(t *testing.T, callback func(context.Context, *Engine, Delta)) *Engine {
	t.Helper()
	var engine *Engine
	var err error
	engine, err = NewEngine(EngineConfig{DeltaListeners: []DeltaListener{
		DeltaListenerFunc(func(ctx context.Context, delta Delta) { callback(ctx, engine, delta) }),
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), &engineTestDispatcher{policy: ReplayPolicyNever})
	input, err := EncodeInput(engineTestInput{Value: "stream"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), deployment, input)
	if err != nil || result.Status() != StatusCompleted {
		t.Fatalf("Run = %s, %v", result.Status(), err)
	}
	return engine
}
