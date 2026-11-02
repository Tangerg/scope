package conformancetest

import (
	startrand "crypto/rand"

	"context"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// CheckDispatcherRejection verifies that malformed protocol input settles at
// the Runtime boundary and can be consumed without host adjudication.
func CheckDispatcherRejection(t *testing.T, dispatcher run.Dispatcher, effect run.Effect) {
	t.Helper()
	recorder := &rejectionDispatcher{next: dispatcher, outcome: make(chan dispatchOutcome, 1)}
	deployment := newRejectionDeployment(t, recorder, effect)
	engine, err := run.New(context.Background(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), rejectionTimeout)
	defer cancel()
	defer func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	input, err := run.EncodePayload(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(ctx, deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(t.Context()), rejectionTimeout)
		defer cleanupCancel()
		if joinErr := process.Join(cleanupCtx); joinErr != nil {
			t.Error(joinErr)
		}
	}()
	var outcome dispatchOutcome
	select {
	case outcome = <-recorder.outcome:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !outcome.definiteFailure() {
		t.Fatalf("local rejection settlement = %+v, error = %v; want definite failed settlement", outcome.settlement, outcome.err)
	}
	result, err := process.Await(ctx)
	if err != nil || result.Termination().Status() != run.StatusCompleted || len(result.Termination().UnresolvedEffectIDs()) != 0 || result.Usage().PreparedEffects != 1 {
		t.Fatalf("rejection consumption = %+v, error = %v", result, err)
	}
}

const (
	rejectionStateKind = "conformance.dispatch"
	rejectionTimeout   = 5 * time.Second
)

func newRejectionDeployment(t *testing.T, dispatcher run.Dispatcher, effect run.Effect) run.Program[run.Payload, run.Payload] {
	t.Helper()
	schema, err := jsonschema.For[struct{}]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: rejectionStateKind, Description: "Exercise a rejected dispatcher protocol.", InputSchema: schema, OutputSchema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: rejectionDefinition{descriptor: descriptor, effect: effect}, Dispatcher: dispatcher,
		ImplementationDigest: run.ComputeDigest([]byte("dispatcher-rejection")), ConfigurationDigest: run.ComputeDigest(effect.Payload()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

type dispatchOutcome struct {
	settlement run.Settlement
	err        error
}

func (d dispatchOutcome) definiteFailure() bool {
	return d.err == nil && d.settlement.Valid() &&
		d.settlement.Status() == run.SettlementStatusFailed
}

type rejectionDispatcher struct {
	next    run.Dispatcher
	outcome chan dispatchOutcome
}

func (r *rejectionDispatcher) Policy(effect run.Effect) run.EffectPolicy {
	return r.next.Policy(effect)
}
func (r *rejectionDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, emit run.DeltaEmitter) (run.Settlement, error) {
	settlement, err := r.next.Dispatch(ctx, request, emit)
	r.outcome <- dispatchOutcome{settlement: settlement, err: err}
	return settlement, err
}

type rejectionDefinition struct {
	descriptor run.Descriptor
	effect     run.Effect
}

func (r rejectionDefinition) Descriptor() run.Descriptor { return r.descriptor }

func (rejectionDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	return nil
}
func (r rejectionDefinition) Start(_ run.Payload) (run.Execution, error) {
	return &rejectionExecution{effect: r.effect}, nil
}
func (r rejectionDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sent, err := state.Decode[bool](rejectionStateKind)
	if err != nil {
		return nil, err
	}
	return &rejectionExecution{effect: r.effect, sent: sent}, nil
}

type rejectionExecution struct {
	effect run.Effect
	sent   bool
}

func (r *rejectionExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(rejectionStateKind, r.sent)
}
func (r *rejectionExecution) Step(_ context.Context, signals []run.Signal) (run.Transition, error) {
	if !r.sent {
		r.sent = true
		return run.Continue(0, r.effect)
	}
	output, err := run.EncodePayload(struct{}{})
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(uint32(len(signals)), output)
}
