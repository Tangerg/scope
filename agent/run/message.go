package run

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var (
	ErrInvalidMessage = errors.New("run: invalid message")
	ErrSignalRejected = errors.New("run: signal rejected")
	ErrSignalConflict = errors.New("run: signal identity conflict")
)

type Message interface{ message() }

type Signal struct {
	ID      SignalID `json:"id"`
	Wait    WaitKey  `json:"wait,omitzero"`
	Payload Payload  `json:"payload"`
}

func (Signal) message()      {}
func (s Signal) Valid() bool { return s.ID.Valid() && s.Payload.Valid() }

// digest is taken only of admitted or validated Signals, and a valid Signal
// always encodes, so the encoding error cannot occur.
func (s Signal) digest() Digest { encoded, _ := jsonv2.Marshal(s); return ComputeDigest(encoded) }

func (s Signal) receipt() SignalReceipt {
	return SignalReceipt{ID: s.ID, Wait: s.Wait, Digest: s.digest()}
}

func (s Signal) equal(other Signal) bool {
	return s.ID == other.ID && s.Wait == other.Wait && s.Payload.Equal(other.Payload)
}

type Completion struct {
	Effect  EffectID   `json:"effect"`
	Outcome Settlement `json:"outcome"`
}

func (Completion) message() {}

func (c Completion) Valid() bool {
	return c.Effect.Valid() && c.Outcome.Valid() && c.Outcome.Status() != SettlementStatusUnknown
}

type Exit struct {
	Child       ProcessID   `json:"child"`
	Termination Termination `json:"termination"`
}

func (Exit) message()      {}
func (e Exit) Valid() bool { return e.Child.Valid() && e.Termination.Valid() }

type Inbox []Message

func (i Inbox) MarshalJSON() ([]byte, error) {
	wire := make([]messageWire, len(i))
	for index, message := range i {
		switch value := message.(type) {
		case Signal:
			if !value.Valid() {
				return nil, ErrInvalidMessage
			}
			wire[index].Signal = &value
		case Completion:
			if !value.Valid() {
				return nil, ErrInvalidMessage
			}
			wire[index].Completion = &value
		case Exit:
			if !value.Valid() {
				return nil, ErrInvalidMessage
			}
			wire[index].Exit = &value
		default:
			return nil, ErrInvalidMessage
		}
	}
	return jsonv2.Marshal(wire)
}

func (i *Inbox) UnmarshalJSON(data []byte) error {
	if i == nil {
		return ErrInvalidMessage
	}
	wire, err := jsonwire.Decode[[]json.RawMessage](data)
	if err != nil {
		return err
	}
	value := make(Inbox, len(wire))
	for index, raw := range wire {
		item, decodeErr := jsonwire.Decode[messageWire](raw)
		if decodeErr != nil {
			return decodeErr
		}
		count := 0
		if item.Signal != nil {
			if !item.Signal.Valid() {
				return ErrInvalidMessage
			}
			value[index] = *item.Signal
			count++
		}
		if item.Completion != nil {
			if !item.Completion.Valid() {
				return ErrInvalidMessage
			}
			value[index] = *item.Completion
			count++
		}
		if item.Exit != nil {
			if !item.Exit.Valid() {
				return ErrInvalidMessage
			}
			value[index] = *item.Exit
			count++
		}
		if count != 1 {
			return fmt.Errorf("%w: exactly one message variant is required", ErrInvalidMessage)
		}
	}
	*i = value
	return nil
}

type messageWire struct {
	Signal     *Signal     `json:"signal,omitzero"`
	Completion *Completion `json:"completion,omitzero"`
	Exit       *Exit       `json:"exit,omitzero"`
}

type signalBatch []Signal

func (s signalBatch) validate() error {
	if len(s) == 0 {
		return ErrInvalidMessage
	}
	ids := make(map[SignalID]struct{}, len(s))
	waits := make(map[WaitKey]struct{})
	for _, signal := range s {
		if !signal.Valid() {
			return ErrInvalidMessage
		}
		if _, duplicate := ids[signal.ID]; duplicate {
			return ErrSignalConflict
		}
		ids[signal.ID] = struct{}{}
		if signal.Wait.Valid() {
			if _, duplicate := waits[signal.Wait]; duplicate {
				return ErrSignalConflict
			}
			waits[signal.Wait] = struct{}{}
		}
	}
	return nil
}

func (s signalBatch) unadmitted(receipts map[SignalID]SignalReceipt) ([]Signal, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	var fresh []Signal
	for _, signal := range s {
		if receipt, exists := receipts[signal.ID]; exists {
			if receipt.Digest != signal.digest() {
				return nil, ErrSignalConflict
			}
		} else {
			fresh = append(fresh, signal)
		}
	}
	return fresh, nil
}
