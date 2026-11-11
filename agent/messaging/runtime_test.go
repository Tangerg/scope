package messaging

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func require[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

type senderDefinition struct{ descriptor run.Descriptor }

func (s senderDefinition) Descriptor() run.Descriptor                           { return s.descriptor }
func (senderDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (s senderDefinition) Start(input run.Payload) (run.Execution, error) {
	message, err := input.Decode[Message]()
	if err != nil {
		return nil, err
	}
	return &senderExecution{message: &message}, nil
}

func (s senderDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	execution, err := state.Decode[senderExecution]("test.sender")
	return &execution, err
}

type senderExecution struct {
	message *Message
	pending *run.EffectID
	done    bool
}

type senderWire struct {
	Message *Message      `json:"message,omitzero"`
	Pending *run.EffectID `json:"pending,omitzero"`
	Done    bool          `json:"done,omitzero"`
}

func (s senderExecution) MarshalJSON() ([]byte, error) {
	payload, err := run.EncodePayload(senderWire{Message: s.message, Pending: s.pending, Done: s.done})
	return payload.JSON(), err
}

func (s *senderExecution) UnmarshalJSON(data []byte) error {
	payload, err := run.ParsePayload(data)
	if err != nil {
		return err
	}
	wire, err := payload.Decode[senderWire]()
	if err != nil {
		return err
	}
	*s = senderExecution{message: wire.Message, pending: wire.Pending, done: wire.Done}
	return nil
}

func (s *senderExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState("test.sender", s)
}

func (s *senderExecution) Step(_ context.Context, turn run.Turn) (run.Transition, error) {
	if s.done {
		return run.Transition{}, ErrInvalidMessage
	}
	if s.pending == nil {
		effect, err := s.message.Effect()
		if err != nil {
			return run.Transition{}, err
		}
		s.pending = new(turn.EffectID(0))
		s.message = nil
		return run.Yield(0, []run.Effect{effect})
	}
	if len(turn.Messages) != 1 {
		return run.Transition{}, ErrInvalidMessage
	}
	completion, ok := turn.Messages[0].(run.Completion)
	if !ok || completion.Effect != *s.pending {
		return run.Transition{}, ErrInvalidMessage
	}
	payload := completion.Outcome.Payload()
	s.pending = nil
	s.done = true
	if completion.Outcome.Status() == run.SettlementStatusFailed {
		failure, valid := completion.Outcome.Failure()
		if !valid {
			return run.Transition{}, run.ErrInvalidSettlement
		}
		return run.Fail(1, failure)
	}
	return run.Complete(1, payload)
}

func senderProgram(t *testing.T, runtime *run.Runtime, port DeliveryPort) run.Program[Message, Receipt] {
	t.Helper()
	descriptor := require(run.NewDescriptor(run.DescriptorConfig{Name: "test.sender", Description: "Deliver one message.", InputSchema: require(jsonschema.For[Message]()), OutputSchema: require(jsonschema.For[Receipt]())}))
	dispatcher := require(NewDispatcher(DispatcherConfig{Port: port}))
	return require(run.Bind[Message, Receipt](runtime.Compiler(), run.BindingConfig{Definition: senderDefinition{descriptor: descriptor}, Dispatcher: dispatcher, Revision: "test-delivery"}))
}

type recipientPort struct {
	runtime   *run.Runtime
	store     run.Store
	mu        sync.Mutex
	calls     []run.Signal
	loseFirst bool
}

func (r *recipientPort) Deliver(ctx context.Context, sender, recipient run.ProcessID, signal run.Signal) error {
	if !sender.Valid() || !recipient.Valid() || !signal.Valid() {
		return ErrInvalidMessage
	}
	r.mu.Lock()
	r.calls = append(r.calls, signal)
	first := len(r.calls) == 1
	r.mu.Unlock()
	log, found, err := r.store.Load(ctx, recipient.Root())
	if err != nil {
		return err
	}
	if found {
		for _, entry := range log.Entries {
			admission, ok := entry.Record.(run.SignalAdmitted)
			if !ok || admission.Process != recipient || admission.Signal.ID != signal.ID {
				continue
			}
			if admission.Signal.Wait != signal.Wait || !bytes.Equal(admission.Signal.Payload.JSON(), signal.Payload.JSON()) {
				return run.ErrSignalConflict
			}
			return nil
		}
	}
	target, found := r.runtime.Process(recipient)
	if !found {
		return run.ErrProcessNotFound
	}
	if _, err := target.DeliverSignals(ctx, signal); err != nil {
		return err
	}
	if first && r.loseFirst {
		return errors.New("delivery acknowledgment lost")
	}
	return nil
}

func (r *recipientPort) recorded() []run.Signal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]run.Signal(nil), r.calls...)
}

func TestMessageDeliveryReconcilesLostAcknowledgmentWithSameSignalIdentity(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	schema := require(jsonschema.For[string]())
	gate := require(coordination.Compile[string, string](runtime.Compiler(), coordination.Config{Name: "test.recipient", Description: "Receive one answer.", Policy: coordination.InputGate{RequestSchema: schema, AnswerSchema: schema}}))
	recipient := require(runtime.Start(t.Context(), gate, run.StartRequest[string]{ID: run.StartID{Scope: "recipient", Key: t.Name()}, Input: "question"}))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	var key run.WaitKey
	for !key.Valid() {
		view := require(recipient.Inspect(ctx))
		process, _ := view.Process(recipient.ID())
		if len(process.Waits) == 1 {
			key = process.Waits[0].Key
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	port := &recipientPort{runtime: runtime, store: store, loseFirst: true}
	sender := senderProgram(t, runtime, port)
	message := Message{Recipient: recipient.ID(), WaitKey: key, Payload: require(run.EncodePayload("answer"))}
	handle := require(runtime.Start(ctx, sender, run.StartRequest[Message]{ID: run.StartID{Scope: "sender", Key: t.Name()}, Input: message}))
	var unknown run.EffectID
	for !unknown.Valid() {
		view := require(handle.Inspect(ctx))
		process, _ := view.Process(handle.ID())
		for _, pending := range process.PendingEffects {
			if pending.Outcome.Status() == run.SettlementStatusUnknown {
				unknown = pending.Request.ID()
				if pending.Policy.Replay != run.ReplayPolicySameIdentity {
					t.Fatal("delivery cannot replay by recipient receipt")
				}
			}
		}
		if unknown.Valid() {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	recipientResult := require(recipient.Await(ctx))
	answer := require(recipientResult.Output())
	if answer != "answer" {
		t.Fatal("recipient answer changed")
	}
	if err := handle.ReplayUnknownEffect(ctx, unknown); err != nil {
		t.Fatal(err)
	}
	result := require(handle.Await(ctx))
	receipt := require(result.Output())
	calls := port.recorded()
	if len(calls) != 2 || calls[0].ID != calls[1].ID || receipt.SignalID != calls[0].ID {
		t.Fatalf("replay changed delivery identity: %+v", calls)
	}
	log, _, err := store.Load(ctx, recipient.ID())
	if err != nil {
		t.Fatal(err)
	}
	admissions := 0
	for _, entry := range log.Entries {
		if _, ok := entry.Record.(run.SignalAdmitted); ok {
			admissions++
		}
	}
	if admissions != 1 {
		t.Fatalf("recipient admitted %d copies", admissions)
	}
}

func TestMessageEffectFreezesRecipientAndWait(t *testing.T) {
	recipient := require(run.ParseProcessID("recipient"))
	key := require(run.ParseWaitKey("question"))
	message := Message{Recipient: recipient, WaitKey: key, Payload: require(run.EncodePayload("answer"))}
	effect := require(message.Effect())
	message.WaitKey = require(run.ParseWaitKey("changed"))
	message.Recipient = require(run.ParseProcessID("other"))
	message.Payload = require(run.EncodePayload("different"))
	frozen := require(decodeMessage(effect))
	if frozen.Recipient != recipient || frozen.WaitKey.String() != "question" || require(frozen.Payload.Decode[string]()) != "answer" {
		t.Fatal("effect retained writable caller configuration")
	}
	if _, err := NewDispatcher(DispatcherConfig{}); !errors.Is(err, ErrNilDeliveryPort) {
		t.Fatal(err)
	}
	if _, err := (Message{}).Effect(); !errors.Is(err, ErrInvalidMessage) {
		t.Fatal(err)
	}
	for _, raw := range []string{`null`, `{}`, `{"recipient":"recipient","payload":null,"unknown":true}`} {
		effect := require(run.NewDispatcherEffect([]byte(raw)))
		if _, err := decodeMessage(effect); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
