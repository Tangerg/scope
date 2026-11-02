package run

import "context"

// Definition and Execution perform no external I/O. Their inputs contain every
// fact needed to replay a Step; clocks, randomness and Host context values must
// not influence a transition. Each restored transition is checked against its log.
type Definition interface {
	Descriptor() Descriptor
	ChildPrograms() []Program[Payload, Payload]
	Start(input Payload) (Execution, error)
	Restore(ctx context.Context, state ExecutionState) (Execution, error)
}

type Execution interface {
	Step(ctx context.Context, turn Turn) (Transition, error)
	Snapshot() (ExecutionState, error)
}

type Turn struct {
	Process  ProcessID
	Step     uint64
	Messages []Message
}

func (t Turn) EffectID(index uint32) EffectID {
	return EffectID{Process: t.Process, Step: t.Step, Index: index}
}

func (t Turn) Valid() bool {
	if !t.Process.Valid() || t.Step == 0 {
		return false
	}
	for _, message := range t.Messages {
		switch value := message.(type) {
		case Signal:
			if !value.Valid() {
				return false
			}
		case Completion:
			if !value.Valid() {
				return false
			}
		case Exit:
			if !value.Valid() {
				return false
			}
		default:
			return false
		}
	}
	return true
}
