package messaging_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/messaging"
	"github.com/Tangerg/scope/agent/strategy/coordination"
)

func bind(t testing.TB, definition agent.Definition, dispatcher agent.Dispatcher) agent.Deployment {
	t.Helper()
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("messaging-fixture")),
		ConfigurationDigest:  agent.ComputeDigest([]byte(t.Name() + "/" + definition.Descriptor().Name())),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func input[T any](t testing.TB, value T) agent.Input {
	t.Helper()
	encoded, err := agent.EncodeInput(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func inspect(t testing.TB, engine *agent.Engine, process *agent.Process) agent.ProcessSnapshot {
	t.Helper()
	tree, err := engine.InspectTree(context.Background(), process.Relation().RootID())
	if err != nil {
		t.Fatal(err)
	}
	fact, present := tree.Process(process.ID())
	if !present {
		t.Fatal("process is absent")
	}
	return fact.Snapshot
}

func newGate(t testing.TB) *coordination.InputGate {
	t.Helper()
	schema, err := agent.SchemaFor[string]()
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

type senderDefinition struct{ descriptor agent.Descriptor }
type senderState struct {
	Message messaging.Message `json:"message"`
	Sent    bool              `json:"sent"`
}
type senderExecution struct{ state senderState }

func newSender(t testing.TB) senderDefinition {
	t.Helper()
	in, err := agent.SchemaFor[messaging.Message]()
	if err != nil {
		t.Fatal(err)
	}
	out, err := agent.SchemaFor[messaging.Receipt]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: "test.reviewer", Description: "Deliver a review and retain its admission receipt.", InputSchema: in, OutputSchema: out,
	})
	if err != nil {
		t.Fatal(err)
	}
	return senderDefinition{descriptor: descriptor}
}

func (s senderDefinition) Descriptor() agent.Descriptor { return s.descriptor }
func (s senderDefinition) Start(input agent.Input) (agent.Execution, error) {
	message, err := input.Decode[messaging.Message]()
	if err != nil {
		return nil, err
	}
	if !message.Valid() {
		return nil, messaging.ErrInvalidMessage
	}
	return &senderExecution{state: senderState{Message: message}}, nil
}
func (s senderDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if state.Kind() != "test.sender" {
		return nil, agent.ErrInvalidExecutionState
	}
	input, err := agent.ParseInput(state.Payload())
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
func (s *senderExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	if !s.state.Sent {
		effect, err := s.state.Message.Effect()
		if err != nil {
			return agent.Transition{}, err
		}
		s.state.Sent = true
		return agent.Continue(0, effect)
	}
	if len(signals) != 1 {
		return agent.Transition{}, errors.New("review requires one receipt")
	}
	output, err := agent.ParseOutput(signals[0].Payload())
	if err != nil {
		return agent.Transition{}, err
	}
	receipt, err := output.Decode[messaging.Receipt]()
	if err != nil {
		return agent.Transition{}, err
	}
	if receipt.Recipient != s.state.Message.Recipient || !receipt.SignalID.Valid() {
		return agent.Transition{}, messaging.ErrInvalidMessage
	}
	return agent.Complete(1, output)
}
func (s *senderExecution) Snapshot() (agent.ExecutionState, error) {
	encoded, err := agent.EncodeInput(s.state)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState("test.sender", encoded.JSON())
}

// recipientPort grants this bound reviewer access to one concrete mailbox.
// Inspection can reconcile a terminal recipient without changing its address.
type recipientPort struct {
	engine             *agent.Engine
	recipient          *agent.Process
	mu                 sync.Mutex
	calls              []agent.SignalRequest
	firstAdmission     chan struct{}
	release            chan struct{}
	lostAcknowledgment bool
	checkReceipts      bool
}

func (r *recipientPort) Deliver(ctx context.Context, sender, recipient agent.ProcessID, signal agent.SignalRequest) error {
	if !sender.Valid() || recipient != r.recipient.ID() {
		return agent.ErrSignalRejected
	}
	payload, err := agent.ParseInput(signal.Payload())
	if err != nil {
		return err
	}
	if _, decodeErr := payload.Decode[string](); decodeErr != nil {
		return decodeErr
	}
	r.mu.Lock()
	r.calls = append(r.calls, signal)
	first := len(r.calls) == 1
	r.mu.Unlock()
	if r.checkReceipts {
		tree, inspectErr := r.engine.InspectTree(ctx, r.recipient.Relation().RootID())
		if inspectErr != nil {
			return inspectErr
		}
		fact, present := tree.Process(recipient)
		if !present {
			return agent.ErrProcessNotRunning
		}
		for _, receipt := range fact.Snapshot.SignalReceipts() {
			if receipt.ID() != signal.ID() {
				continue
			}
			if !receipt.Matches(signal) {
				return agent.ErrSignalConflict
			}
			return nil
		}
	}
	_, err = r.recipient.DeliverSignals(ctx, signal)
	if err != nil {
		return err
	}
	if first && r.firstAdmission != nil {
		close(r.firstAdmission)
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if first && r.lostAcknowledgment {
		return errors.New("delivery acknowledgment lost")
	}
	return nil
}

func finish(t testing.TB, process *agent.Process) agent.Result {
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

func closeEngine(t testing.TB, engine *agent.Engine) {
	t.Helper()
	if err := engine.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
