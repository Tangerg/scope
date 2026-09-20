package conformancetest

import (
	"context"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
)

// CheckDispatcherRejection verifies that malformed protocol input settles at
// the Engine boundary and can be consumed without host adjudication.
func CheckDispatcherRejection(t *testing.T, dispatcher agent.Dispatcher, effect agent.Effect) {
	t.Helper()
	schema, err := agent.SchemaFor[struct{}]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: "conformance.dispatch", Description: "Exercise a rejected dispatcher protocol.", InputSchema: schema, OutputSchema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &rejectionDispatcher{next: dispatcher, outcome: make(chan dispatchOutcome, 1)}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: rejectionDefinition{descriptor: descriptor, effect: effect}, Dispatcher: recorder,
		ImplementationDigest: agent.ComputeDigest([]byte("dispatcher-rejection")), ConfigurationDigest: agent.ComputeDigest(effect.Payload()),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	defer func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	input, err := agent.EncodePayload(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(ctx, deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
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
	if outcome.err != nil || !outcome.settlement.Valid() || outcome.settlement.Status() != agent.SettlementStatusFailed || outcome.settlement.EffectID() != outcome.id {
		t.Fatalf("local rejection settlement = %+v, error = %v; want definite failed settlement", outcome.settlement, outcome.err)
	}
	result, err := process.Await(ctx)
	if err != nil || result.Status() != agent.StatusCompleted || len(result.Termination().UnresolvedEffectIDs()) != 0 || result.Usage().PreparedEffects != 1 {
		t.Fatalf("rejection consumption = %+v, error = %v", result, err)
	}
}

type dispatchOutcome struct {
	id         agent.EffectID
	settlement agent.Settlement
	err        error
}

type rejectionDispatcher struct {
	next    agent.Dispatcher
	outcome chan dispatchOutcome
}

func (r *rejectionDispatcher) ReplayPolicy(effect agent.Effect) agent.ReplayPolicy {
	return r.next.ReplayPolicy(effect)
}
func (r *rejectionDispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	settlement, err := r.next.Dispatch(ctx, request, emit)
	r.outcome <- dispatchOutcome{id: request.ID(), settlement: settlement, err: err}
	return settlement, err
}

type rejectionDefinition struct {
	descriptor agent.Descriptor
	effect     agent.Effect
}

func (r rejectionDefinition) Descriptor() agent.Descriptor { return r.descriptor }
func (r rejectionDefinition) Start(_ agent.Payload) (agent.Execution, error) {
	return &rejectionExecution{effect: r.effect}, nil
}
func (r rejectionDefinition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sent, err := state.Decode[bool]("conformance.dispatch")
	if err != nil {
		return nil, err
	}
	return &rejectionExecution{effect: r.effect, sent: sent}, nil
}

type rejectionExecution struct {
	effect agent.Effect
	sent   bool
}

func (r *rejectionExecution) Snapshot() (agent.ExecutionState, error) {
	return agent.EncodeExecutionState("conformance.dispatch", r.sent)
}
func (r *rejectionExecution) Step(_ context.Context, signals []agent.Signal) (agent.Transition, error) {
	if !r.sent {
		r.sent = true
		return agent.Continue(0, r.effect)
	}
	output, err := agent.EncodePayload(struct{}{})
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Complete(uint32(len(signals)), output)
}
