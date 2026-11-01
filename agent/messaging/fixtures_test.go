package messaging_test

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/messaging"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

const senderStateKind = "test.sender"

func bind(t testing.TB, definition run.Definition, dispatcher run.Dispatcher) run.Program[run.Payload, run.Payload] {
	t.Helper()
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: run.ComputeDigest([]byte("messaging-fixture")),
		ConfigurationDigest:  run.ComputeDigest([]byte(t.Name() + "/" + definition.Descriptor().Name())),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func newEngine(t testing.TB, store run.Store) *run.Runtime {
	t.Helper()
	engine, err := run.New(t.Context(), run.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func newDispatcher(t testing.TB, port messaging.DeliveryPort) *messaging.Dispatcher {
	t.Helper()
	dispatcher, err := messaging.NewDispatcher(messaging.DispatcherConfig{Port: port})
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func start(t testing.TB, engine *run.Runtime, deployment run.Program[run.Payload, run.Payload], payload run.Payload) *run.Handle[run.Payload] {
	t.Helper()
	process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: payload})
	if err != nil {
		t.Fatal(err)
	}
	return process
}

func input[T any](t testing.TB, value T) run.Payload {
	t.Helper()
	encoded, err := run.EncodePayload(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func inspect(t testing.TB, engine *run.Runtime, process *run.Handle[run.Payload]) run.ProcessSnapshot {
	t.Helper()
	tree, err := engine.InspectTree(context.Background(), process.Relation().RootID())
	if err != nil {
		t.Fatal(err)
	}
	fact, present := tree.Process(process.Relation().ProcessID())
	if !present {
		t.Fatal("process is absent")
	}
	return fact.Snapshot
}

func newGate(t testing.TB) *coordination.InputGate {
	t.Helper()
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	gate, err := coordination.NewInputGate(coordination.InputGateConfig{
		Name: "test.mailbox", Description: "Consume one review message.", RequestSchema: schema, AnswerSchema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	return gate
}

type senderDefinition struct{ descriptor run.Descriptor }
type senderState struct {
	Message messaging.Message `json:"message"`
	Sent    bool              `json:"sent"`
}
type senderExecution struct{ state senderState }

func newSender(t testing.TB) senderDefinition {
	t.Helper()
	in, err := jsonschema.For[messaging.Message]()
	if err != nil {
		t.Fatal(err)
	}
	out, err := jsonschema.For[messaging.Receipt]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: "test.reviewer", Description: "Deliver a review and retain its admission receipt.", InputSchema: in, OutputSchema: out,
	})
	if err != nil {
		t.Fatal(err)
	}
	return senderDefinition{descriptor: descriptor}
}

func (s senderDefinition) Descriptor() run.Descriptor { return s.descriptor }

func (senderDefinition) ChildDeployments() []run.Program[run.Payload, run.Payload] { return nil }
func (s senderDefinition) Start(input run.Payload) (run.Execution, error) {
	message, err := input.Decode[messaging.Message]()
	if err != nil {
		return nil, err
	}
	if !message.Valid() {
		return nil, messaging.ErrInvalidMessage
	}
	return &senderExecution{state: senderState{Message: message}}, nil
}
func (s senderDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if state.Kind() != senderStateKind {
		return nil, run.ErrInvalidExecutionState
	}
	input, err := run.ParsePayload(state.Payload())
	if err != nil {
		return nil, err
	}
	decoded, err := input.Decode[senderState]()
	if err != nil {
		return nil, err
	}
	if !decoded.Message.Valid() {
		return nil, messaging.ErrInvalidMessage
	}
	return &senderExecution{state: decoded}, nil
}
func (s *senderExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	if !s.state.Sent {
		effect, err := s.state.Message.Effect()
		if err != nil {
			return run.Transition{}, err
		}
		s.state.Sent = true
		return run.Continue(0, effect)
	}
	if len(signals) != 1 {
		return run.Transition{}, errors.New("review requires one receipt")
	}
	output, err := run.ParsePayload(signals[0].Payload())
	if err != nil {
		return run.Transition{}, err
	}
	receipt, err := output.Decode[messaging.Receipt]()
	if err != nil {
		return run.Transition{}, err
	}
	if !receipt.SignalID.Valid() {
		return run.Transition{}, messaging.ErrInvalidMessage
	}
	return run.Complete(1, output)
}
func (s *senderExecution) Snapshot() (run.ExecutionState, error) {
	encoded, err := run.EncodePayload(s.state)
	if err != nil {
		return run.ExecutionState{}, err
	}
	return run.ParseExecutionState(senderStateKind, encoded.JSON())
}

type recipientPort struct {
	engine             *run.Runtime
	recipient          *run.Handle[run.Payload]
	mu                 sync.Mutex
	calls              []run.SignalRequest
	firstAdmission     chan struct{}
	release            chan struct{}
	lostAcknowledgment bool
	checkReceipts      bool
}

func (r *recipientPort) Deliver(ctx context.Context, sender, recipient run.ProcessID, signal run.SignalRequest) error {
	if !sender.Valid() || recipient != r.recipient.Relation().ProcessID() {
		return run.ErrSignalRejected
	}
	payload, err := run.ParsePayload(signal.Payload())
	if err != nil {
		return err
	}
	if _, decodeErr := payload.Decode[string](); decodeErr != nil {
		return decodeErr
	}
	first := r.record(signal)
	if r.checkReceipts {
		if admitted, reconcileErr := r.reconcile(ctx, signal); admitted || reconcileErr != nil {
			return reconcileErr
		}
	}
	if _, err := r.recipient.DeliverSignals(ctx, signal); err != nil {
		return err
	}
	if !first {
		return nil
	}
	if err := r.holdFirstAdmission(ctx); err != nil {
		return err
	}
	if r.lostAcknowledgment {
		return errors.New("delivery acknowledgment lost")
	}
	return nil
}

func (r *recipientPort) holdFirstAdmission(ctx context.Context) error {
	if r.firstAdmission == nil {
		return nil
	}
	close(r.firstAdmission)
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *recipientPort) record(signal run.SignalRequest) (first bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, signal)
	return len(r.calls) == 1
}

func (r *recipientPort) recordedCalls() []run.SignalRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// reconcile proves an earlier admission from the recipient's authoritative
// receipts, which remain after the recipient has consumed or finished.
func (r *recipientPort) reconcile(ctx context.Context, signal run.SignalRequest) (bool, error) {
	tree, err := r.engine.InspectTree(ctx, r.recipient.Relation().RootID())
	if err != nil {
		return false, err
	}
	fact, present := tree.Process(r.recipient.Relation().ProcessID())
	if !present {
		return false, errors.New("recipient missing from tree inspection")
	}
	for _, receipt := range fact.Snapshot.SignalReceipts() {
		if receipt.ID() != signal.ID() {
			continue
		}
		if !receipt.Matches(signal) {
			return false, run.ErrSignalConflict
		}
		return true, nil
	}
	return false, nil
}

func finish(t testing.TB, process *run.Handle[run.Payload]) run.Result[run.Payload] {
	t.Helper()
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if joinErr := process.Join(context.Background()); joinErr != nil {
		t.Fatal(joinErr)
	}
	return result
}

func closeEngine(t testing.TB, engine *run.Runtime) {
	t.Helper()
	if err := engine.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
