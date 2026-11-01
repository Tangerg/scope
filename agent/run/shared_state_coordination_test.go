package run_test

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
	"github.com/Tangerg/scope/core/jsonschema"
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

type revisionDefinition struct{ descriptor run.Descriptor }
type revisionExecution struct {
	Request   revisionRequest `json:"request"`
	Requested bool            `json:"requested"`
}

func (r revisionDefinition) Descriptor() run.Descriptor { return r.descriptor }

func (revisionDefinition) ChildDeployments() []run.Program[run.Payload, run.Payload] {
	return nil
}
func (r revisionDefinition) Start(input run.Payload) (run.Execution, error) {
	request, err := input.Decode[revisionRequest]()
	if err != nil {
		return nil, err
	}
	return &revisionExecution{Request: request}, nil
}
func (r revisionDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if state.Kind() != "example.revision" {
		return nil, run.ErrInvalidExecutionState
	}
	input, err := run.ParsePayload(state.Payload())
	if err != nil {
		return nil, err
	}
	execution, err := input.Decode[revisionExecution]()
	if err != nil {
		return nil, err
	}
	return &execution, nil
}
func (r *revisionExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	if !r.Requested {
		payload, err := run.EncodePayload(r.Request)
		if err != nil {
			return run.Transition{}, err
		}
		effect, err := run.NewDispatcherEffect(payload.JSON())
		if err != nil {
			return run.Transition{}, err
		}
		r.Requested = true
		return run.Continue(0, effect)
	}
	if len(signals) != 1 {
		return run.Transition{}, errors.New("revision update requires one observation")
	}
	output, err := run.ParsePayload(signals[0].Payload())
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(1, output)
}
func (r *revisionExecution) Snapshot() (run.ExecutionState, error) {
	payload, err := run.EncodePayload(r)
	if err != nil {
		return run.ExecutionState{}, err
	}
	return run.ParseExecutionState("example.revision", payload.JSON())
}

// Shared data and idempotency facts belong to the external store. Revision
// conflicts are business observations, carried into Step through settlement.
type revisionStore struct {
	mu       sync.Mutex
	current  revisionObservation
	facts    map[run.EffectID]revisionRecord
	attempts int
}
type revisionRecord struct {
	request    run.Digest
	settlement run.Settlement
}

func (r *revisionStore) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicySameIdentity}
}
func (r *revisionStore) Dispatch(ctx context.Context, request run.EffectRequest, _ run.DeltaEmitter) (run.Settlement, error) {
	if err := ctx.Err(); err != nil {
		return run.Settlement{}, err
	}
	input, err := run.ParsePayload(request.Effect().Payload())
	if err != nil {
		return run.Settlement{}, err
	}
	update, err := input.Decode[revisionRequest]()
	if err != nil {
		return run.Settlement{}, err
	}
	digest := run.ComputeDigest(input.JSON())
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	if fact, present := r.facts[request.ID()]; present {
		if fact.request != digest {
			return run.Settlement{}, errors.New("revision operation identity conflict")
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
	payload, err := run.EncodePayload(observation)
	if err != nil {
		return run.Settlement{}, err
	}
	settlement, err := run.NewSettlement(run.SettlementStatusSucceeded, payload.JSON())
	if err != nil {
		return run.Settlement{}, err
	}
	r.facts[request.ID()] = revisionRecord{request: digest, settlement: settlement}
	return settlement, nil
}

type revisionLostAcknowledgment struct {
	*run.MemoryStore
	lost atomic.Bool
}

func (r *revisionLostAcknowledgment) CommitEffect(ctx context.Context, boundary run.EffectBoundary) error {
	if err := r.MemoryStore.CommitEffect(ctx, boundary); err != nil {
		return err
	}
	if boundary.Kind() == run.EffectBoundaryKindSettled && r.lost.CompareAndSwap(false, true) {
		return errors.New("stored revision settlement acknowledgment lost")
	}
	return nil
}

func TestSharedStateCoordinationRestoresObservedRevisionInsteadOfCurrentState(t *testing.T) {
	inputSchema, err := jsonschema.For[revisionRequest]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := jsonschema.For[revisionObservation]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{Name: "example.revision", Description: "Observe one conditional shared-state update.", InputSchema: inputSchema, OutputSchema: outputSchema})
	if err != nil {
		t.Fatal(err)
	}
	definition := revisionDefinition{descriptor: descriptor}
	shared := &revisionStore{facts: make(map[run.EffectID]revisionRecord)}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, Dispatcher: shared,
		ImplementationDigest: run.ComputeDigest([]byte("revision-comparison")), ConfigurationDigest: run.ComputeDigest([]byte("shared-revision-store")),
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := run.EncodePayload(revisionRequest{Value: "first"})
	if err != nil {
		t.Fatal(err)
	}
	runtest.RunDefinitionConformance(t, runtest.DefinitionConformanceConfig{Definition: definition, Input: input})
	store := run.NewMemoryStore()
	engine, err := run.New(t.Context(), run.Config{Store: &revisionLostAcknowledgment{MemoryStore: store}})
	if err != nil {
		t.Fatal(err)
	}
	var competitors []*run.Handle[run.Payload]
	for _, value := range []string{"first", "second"} {
		request, encodeErr := run.EncodePayload(revisionRequest{Value: value})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		process, startErr := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: request})
		if startErr != nil {
			t.Fatal(startErr)
		}
		competitors = append(competitors, process)
	}
	var failed *run.Handle[run.Payload]
	var observations []revisionObservation
	for _, process := range competitors {
		result, awaitErr := process.Await(t.Context())
		if awaitErr != nil {
			if _, runtimeFailure := errors.AsType[*run.RuntimeError](awaitErr); !runtimeFailure || failed != nil {
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
	laterInput, err := run.EncodePayload(revisionRequest{Expected: 1, Value: "later value"})
	if err != nil {
		t.Fatal(err)
	}
	later, err := runtest.Observe(t.Context(), engine, deployment, laterInput)
	if err != nil {
		t.Fatal(err)
	}
	if observed := revisionResult(t, later); !observed.Updated || observed.Revision != 2 || observed.Value != "later value" {
		t.Fatalf("later external state=%+v", observed)
	}
	head, present, loadErr := store.LoadTree(t.Context(), failed.Relation().ProcessID())
	if loadErr != nil || !present {
		t.Fatalf("lost-response head=%t %v", present, loadErr)
	}
	restoredEngine, err := run.New(t.Context(), run.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restoredEngine.Recover(t.Context(), deployment, head)
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

func revisionResult(t testing.TB, result run.Result[run.Payload]) revisionObservation {
	t.Helper()
	output, present := result.Termination().Output()
	if !present || result.Termination().Status() != run.StatusCompleted || result.Usage() != (run.Usage{CommittedSteps: 2, PreparedEffects: 1, AcceptedSignals: 1}) {
		t.Fatalf("revision execution=%s usage=%+v", result.Termination().Status(), result.Usage())
	}
	observation, err := output.Decode[revisionObservation]()
	if err != nil {
		t.Fatal(err)
	}
	return observation
}
