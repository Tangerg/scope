package agent_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
)

type revisionRequest struct {
	Expected uint64 `json:"expected"`
	Value    string `json:"value"`
}

type revisionObservation struct {
	Revision uint64 `json:"revision"`
	Value    string `json:"value"`
	Updated  bool   `json:"updated"`
}

type revisionDefinition struct{ descriptor agent.Descriptor }
type revisionExecution struct {
	Request   revisionRequest `json:"request"`
	Requested bool            `json:"requested"`
}

func (r revisionDefinition) Descriptor() agent.Descriptor { return r.descriptor }
func (r revisionDefinition) Start(input agent.Input) (agent.Execution, error) {
	request, err := input.Decode[revisionRequest]()
	if err != nil {
		return nil, err
	}
	return &revisionExecution{Request: request}, nil
}
func (r revisionDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if state.Kind() != "example.revision" {
		return nil, agent.ErrInvalidExecutionState
	}
	input, err := agent.ParseInput(state.Payload())
	if err != nil {
		return nil, err
	}
	execution, err := input.Decode[revisionExecution]()
	if err != nil {
		return nil, err
	}
	return &execution, nil
}
func (r *revisionExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	if !r.Requested {
		payload, err := agent.EncodeInput(r.Request)
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.NewDispatcherEffect(payload.JSON())
		if err != nil {
			return agent.Transition{}, err
		}
		r.Requested = true
		return agent.Continue(0, effect)
	}
	if len(signals) != 1 {
		return agent.Transition{}, errors.New("revision update requires one observation")
	}
	output, err := agent.ParseOutput(signals[0].Payload())
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Complete(1, output)
}
func (r *revisionExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := agent.EncodeInput(r)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState("example.revision", payload.JSON())
}

// Shared data and idempotency facts belong to the external store. Revision
// conflicts are business observations, carried into Step through settlement.
type revisionStore struct {
	mu       sync.Mutex
	current  revisionObservation
	facts    map[agent.EffectID]revisionStoreFact
	attempts int
}
type revisionStoreFact struct {
	request    agent.Digest
	settlement agent.Settlement
}

func (r *revisionStore) ReplayPolicy(agent.Effect) agent.ReplayPolicy {
	return agent.ReplayPolicySameIdentity
}
func (r *revisionStore) Dispatch(ctx context.Context, request agent.EffectRequest, _ agent.DeltaEmitter) (agent.Settlement, error) {
	if err := ctx.Err(); err != nil {
		return agent.Settlement{}, err
	}
	input, err := agent.ParseInput(request.Effect().Payload())
	if err != nil {
		return agent.Settlement{}, err
	}
	update, err := input.Decode[revisionRequest]()
	if err != nil {
		return agent.Settlement{}, err
	}
	digest := agent.ComputeDigest(input.JSON())
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	if fact, present := r.facts[request.ID()]; present {
		if fact.request != digest {
			return agent.Settlement{}, errors.New("revision operation identity conflict")
		}
		return fact.settlement, nil
	}
	observation := r.current
	observation.Updated = update.Expected == r.current.Revision
	if observation.Updated {
		observation.Revision++
		observation.Value = update.Value
		r.current = observation
	}
	payload, err := agent.EncodeOutput(observation)
	if err != nil {
		return agent.Settlement{}, err
	}
	settlement, err := agent.NewSettlement(request.ID(), agent.SettlementStatusSucceeded, payload.JSON())
	if err != nil {
		return agent.Settlement{}, err
	}
	r.facts[request.ID()] = revisionStoreFact{request: digest, settlement: settlement}
	return settlement, nil
}

type revisionLostAcknowledgment struct {
	*agenttest.MemoryTreeDurability
	lost atomic.Bool
}

func (r *revisionLostAcknowledgment) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	if err := r.MemoryTreeDurability.CommitEffect(ctx, boundary); err != nil {
		return err
	}
	if boundary.Kind() == agent.EffectBoundarySettled && r.lost.CompareAndSwap(false, true) {
		return errors.New("stored revision settlement acknowledgment lost")
	}
	return nil
}

func TestSharedStateCoordinationRestoresObservedRevisionInsteadOfCurrentState(t *testing.T) {
	inputSchema, err := agent.SchemaFor[revisionRequest]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := agent.SchemaFor[revisionObservation]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{Name: "example.revision", Description: "Observe one conditional shared-state update.", InputSchema: inputSchema, OutputSchema: outputSchema})
	if err != nil {
		t.Fatal(err)
	}
	definition := revisionDefinition{descriptor: descriptor}
	shared := &revisionStore{facts: make(map[agent.EffectID]revisionStoreFact)}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: shared,
		ImplementationDigest: agent.ComputeDigest([]byte("revision-comparison")), ConfigurationDigest: agent.ComputeDigest([]byte("shared-revision-store")),
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodeInput(revisionRequest{Value: "first"})
	if err != nil {
		t.Fatal(err)
	}
	agenttest.RunDefinitionConformance(t, agenttest.DefinitionConformanceConfig{Definition: definition, Input: input})
	store := agenttest.NewMemoryTreeDurability()
	engine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: &revisionLostAcknowledgment{MemoryTreeDurability: store}})
	if err != nil {
		t.Fatal(err)
	}
	var competitors []*agent.Process
	for _, value := range []string{"first", "second"} {
		request, encodeErr := agent.EncodeInput(revisionRequest{Value: value})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		process, startErr := engine.Start(t.Context(), deployment, request)
		if startErr != nil {
			t.Fatal(startErr)
		}
		competitors = append(competitors, process)
	}
	var failed *agent.Process
	var observations []revisionObservation
	for _, process := range competitors {
		result, awaitErr := process.Await(t.Context())
		if awaitErr != nil {
			if _, runtimeFailure := errors.AsType[*agent.RuntimeError](awaitErr); !runtimeFailure || failed != nil {
				t.Fatalf("unexpected failure=%v", awaitErr)
			}
			failed = process
		} else {
			observations = append(observations, revisionResult(t, result))
		}
		if joinErr := process.Join(t.Context()); joinErr != nil && process != failed {
			t.Fatal(joinErr)
		}
	}
	if failed == nil {
		t.Fatal("lost acknowledgment did not stop one runtime")
	}
	laterInput, err := agent.EncodeInput(revisionRequest{Expected: 1, Value: "later value"})
	if err != nil {
		t.Fatal(err)
	}
	later, err := engine.Run(t.Context(), deployment, laterInput)
	if err != nil {
		t.Fatal(err)
	}
	if observed := revisionResult(t, later); !observed.Updated || observed.Revision != 2 || observed.Value != "later value" {
		t.Fatalf("later external state=%+v", observed)
	}
	head, present, loadErr := store.LoadTree(t.Context(), failed.ID())
	if loadErr != nil || !present {
		t.Fatalf("lost-response head=%t %v", present, loadErr)
	}
	restoredEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restoredEngine.RestoreTree(t.Context(), deployment, head)
	if err != nil {
		t.Fatal(err)
	}
	result, err := restored.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if joinErr := restored.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	observations = append(observations, revisionResult(t, result))
	if len(observations) != 2 || observations[0].Revision != 1 || observations[1].Revision != 1 || observations[0].Value != observations[1].Value || observations[0].Updated == observations[1].Updated {
		t.Fatalf("conditional updates lost their actual observations: %+v", observations)
	}
	shared.mu.Lock()
	attempts, revision := shared.attempts, shared.current.Revision
	shared.mu.Unlock()
	if attempts != 3 || revision != 2 {
		t.Fatalf("recovery reread or updated current state: attempts=%d revision=%d", attempts, revision)
	}
	for _, closeErr := range []error{engine.Close(t.Context()), restoredEngine.Close(t.Context())} {
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}

func revisionResult(t testing.TB, result agent.Result) revisionObservation {
	t.Helper()
	output, present := result.Output()
	if !present || result.Status() != agent.StatusCompleted || result.Usage() != (agent.Usage{CommittedSteps: 2, PreparedEffects: 1, AcceptedSignals: 1}) {
		t.Fatalf("revision execution=%s usage=%+v", result.Status(), result.Usage())
	}
	observation, err := output.Decode[revisionObservation]()
	if err != nil {
		t.Fatal(err)
	}
	return observation
}
