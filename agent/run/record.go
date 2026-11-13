package run

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"time"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// recordedNow is the instant a record or delta carries: UTC without a
// monotonic reading, so equal instants encode, and therefore digest, identically.
func recordedNow() time.Time { return time.Now().Round(0).UTC() }

// RecordKind is the storage discriminator of a Record; a Store persists it
// beside the encoded body so DecodeRecord can restore the exact variant.
type RecordKind string

const (
	RecordKindStarted          RecordKind = "started"
	RecordKindActivated        RecordKind = "activated"
	RecordKindSignalAdmitted   RecordKind = "signal_admitted"
	RecordKindStepped          RecordKind = "stepped"
	RecordKindEffectIntent     RecordKind = "effect_intent"
	RecordKindEffectSettled    RecordKind = "effect_settled"
	RecordKindExited           RecordKind = "exited"
	RecordKindReaped           RecordKind = "reaped"
	RecordKindCheckpoint       RecordKind = "checkpoint"
	RecordKindControlRequested RecordKind = "control_requested"
	RecordKindExecutionFailed  RecordKind = "execution_failed"
)

// Record is the closed set of facts a tree's log holds. Only this package
// implements it, so a Store can rely on EncodeRecord and DecodeRecord alone.
type Record interface {
	recordKind() RecordKind
	valid() bool
}

type Started struct {
	Process      ProcessID     `json:"process"`
	Program      ProgramRef    `json:"program"`
	Input        Payload       `json:"input"`
	Budget       Budget        `json:"budget" jsonwire:"required"`
	Capabilities CapabilitySet `json:"capabilities" jsonwire:"required"`
	Limits       TreeLimits    `json:"limits,omitzero"`
	At           time.Time     `json:"at"`
}

func (Started) recordKind() RecordKind { return RecordKindStarted }

func (s Started) valid() bool {
	return s.Process.Valid() && s.Program.Valid() && s.Input.Valid() && !s.At.IsZero() && (s.Process.Depth() > 0 && s.Limits == (TreeLimits{}) || s.Process.Depth() == 0 && s.Limits.Valid())
}

// IntentDigest is derived from the accepted declaration. Persisting a separately
// writable digest would let a stale cache accept a conflicting idempotent Start.
func (s Started) IntentDigest() (Digest, error) {
	if s.Process.Depth() != 0 || !s.valid() {
		return Digest{}, ErrInvalidStart
	}
	payload, err := jsonv2.Marshal(startIntent{Program: s.Program, Input: s.Input, Budget: s.Budget, Limits: s.Limits, Capabilities: s.Capabilities}, jsonv2.Deterministic(true))
	if err != nil {
		return Digest{}, err
	}
	return ComputeDigest(payload), nil
}

func (s Started) childSpec() ChildSpec {
	key, _ := s.Process.Key()
	return ChildSpec{Key: key, ProgramRef: s.Program, Input: s.Input, Budget: s.Budget, Capabilities: s.Capabilities}
}

type startIntent struct {
	Program      ProgramRef
	Input        Payload
	Budget       Budget
	Limits       TreeLimits
	Capabilities CapabilitySet
}

type Activated struct {
	Epoch Epoch `json:"epoch"`
}

func (Activated) recordKind() RecordKind { return RecordKindActivated }
func (a Activated) valid() bool          { return a.Epoch > 0 }

type SignalAdmitted struct {
	Process ProcessID `json:"process"`
	Signal  Signal    `json:"signal"`
	At      time.Time `json:"at"`
}

func (SignalAdmitted) recordKind() RecordKind { return RecordKindSignalAdmitted }
func (s SignalAdmitted) valid() bool          { return s.Process.Valid() && s.Signal.Valid() && !s.At.IsZero() }

type Stepped struct {
	Process    ProcessID  `json:"process"`
	Step       uint64     `json:"step"`
	Delivered  uint32     `json:"delivered" jsonwire:"required"`
	Transition Transition `json:"transition"`
}

func (Stepped) recordKind() RecordKind { return RecordKindStepped }

func (s Stepped) valid() bool {
	return s.Process.Valid() && s.Step > 0 && s.Transition.Valid() && s.Transition.Consumed() <= s.Delivered
}

type EffectIntent struct {
	ID     EffectID     `json:"id"`
	Effect Effect       `json:"effect"`
	Policy EffectPolicy `json:"policy"`
	At     time.Time    `json:"at"`
}

func (EffectIntent) recordKind() RecordKind { return RecordKindEffectIntent }

func (e EffectIntent) valid() bool {
	if intrinsic, owned := e.Effect.intrinsicPolicy(); owned && !e.Policy.equal(intrinsic) {
		return false
	}
	return e.ID.Valid() && e.Effect.Valid() && e.Policy.Valid() && !e.At.IsZero()
}

type EffectSettled struct {
	ID      EffectID   `json:"id"`
	Outcome Settlement `json:"outcome"`
	Failure *Failure   `json:"failure,omitzero"`
	At      time.Time  `json:"at"`
}

func (EffectSettled) recordKind() RecordKind { return RecordKindEffectSettled }

func (e EffectSettled) valid() bool {
	return e.ID.Valid() && e.Outcome.Valid() && !e.At.IsZero() && (e.Failure == nil || e.Failure.Valid() && e.Outcome.Status() == SettlementStatusUnknown && e.Outcome.payload.Equal(nullPayload))
}

type Exited struct {
	Process     ProcessID   `json:"process"`
	Termination Termination `json:"termination"`
	At          time.Time   `json:"at"`
}

func (Exited) recordKind() RecordKind { return RecordKindExited }

func (e Exited) valid() bool {
	return e.Process.Valid() && e.Termination.Valid() && !e.At.IsZero()
}

type ExecutionFailed struct {
	Process ProcessID `json:"process"`
	Failure Failure   `json:"failure"`
	At      time.Time `json:"at"`
}

func (ExecutionFailed) recordKind() RecordKind { return RecordKindExecutionFailed }

func (e ExecutionFailed) valid() bool {
	return e.Process.Valid() && e.Failure.Valid() && !e.At.IsZero()
}

type Reaped struct {
	Process ProcessID `json:"process"`
}

func (Reaped) recordKind() RecordKind { return RecordKindReaped }
func (r Reaped) valid() bool          { return r.Process.Valid() && r.Process.Depth() > 0 }

type ControlKind string

const (
	ControlPause    ControlKind = "pause"
	ControlResume   ControlKind = "resume"
	ControlCancel   ControlKind = "cancel"
	ControlKill     ControlKind = "kill"
	ControlDeadline ControlKind = "deadline"
)

func (c ControlKind) Valid() bool {
	switch c {
	case ControlPause, ControlResume, ControlCancel, ControlKill, ControlDeadline:
		return true
	default:
		return false
	}
}

// terminationCause preserves the requester independently of the target path.
func (c ControlKind) terminationCause(origin ControlOrigin) TerminationCause {
	switch {
	case c == ControlKill:
		return TerminationCauseRuntimeKill
	case c == ControlDeadline && origin == ControlOriginHost:
		return TerminationCauseHostDeadline
	case c == ControlDeadline:
		return TerminationCauseParentDeadline
	case origin == ControlOriginHost:
		return TerminationCauseHostCancellation
	default:
		return TerminationCauseParentCancellation
	}
}

// ControlOrigin records who requested a control independently of its target.
// A parent's direct child request must retain its cause across replay.
type ControlOrigin string

const (
	ControlOriginHost   ControlOrigin = "host"
	ControlOriginParent ControlOrigin = "parent"
)

func (c ControlOrigin) Valid() bool {
	return c == ControlOriginHost || c == ControlOriginParent
}

type ControlRequested struct {
	Process ProcessID     `json:"process"`
	Kind    ControlKind   `json:"kind"`
	Origin  ControlOrigin `json:"origin"`
	Reason  string        `json:"reason"`
	At      time.Time     `json:"at"`
}

func (ControlRequested) recordKind() RecordKind { return RecordKindControlRequested }

func (c ControlRequested) valid() bool {
	if !c.Process.Valid() || !c.Kind.Valid() || !c.Origin.Valid() || c.At.IsZero() {
		return false
	}
	if c.Origin == ControlOriginParent && (c.Process.Depth() == 0 || c.Kind != ControlCancel && c.Kind != ControlDeadline) {
		return false
	}
	return c.Kind == ControlResume && c.Reason == "" || c.Kind != ControlResume && validateTerminationReason(c.Reason) == nil
}

func validateRecord(record Record) error {
	if lo.IsNil(record) {
		return ErrInvalidLog
	}
	switch record.(type) {
	case Started, Activated, SignalAdmitted, Stepped, EffectIntent, EffectSettled,
		Exited, Reaped, Checkpoint, ControlRequested, ExecutionFailed:
	default:
		return ErrInvalidLog
	}
	if !record.valid() {
		return ErrInvalidLog
	}
	return nil
}

func EncodeRecord(record Record) (RecordKind, []byte, error) {
	if err := validateRecord(record); err != nil {
		return "", nil, err
	}
	return encodeRecord(record)
}

// Structural admission precedes this encoding so a batch or byte-budget check
// does not traverse a checkpoint's process tree a second time.
func encodeRecord(record Record) (RecordKind, []byte, error) {
	encoded, err := jsonv2.Marshal(record, jsonv2.Deterministic(true))
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrInvalidLog, err)
	}
	return record.recordKind(), encoded, nil
}

func DecodeRecord(kind RecordKind, data []byte) (Record, error) {
	var value Record
	var err error
	switch kind {
	case RecordKindStarted:
		value, err = decodeLogRecord[Started](data)
	case RecordKindActivated:
		value, err = decodeLogRecord[Activated](data)
	case RecordKindSignalAdmitted:
		value, err = decodeLogRecord[SignalAdmitted](data)
	case RecordKindStepped:
		value, err = decodeLogRecord[Stepped](data)
	case RecordKindEffectIntent:
		value, err = decodeLogRecord[EffectIntent](data)
	case RecordKindEffectSettled:
		value, err = decodeLogRecord[EffectSettled](data)
	case RecordKindExited:
		value, err = decodeLogRecord[Exited](data)
	case RecordKindExecutionFailed:
		value, err = decodeLogRecord[ExecutionFailed](data)
	case RecordKindReaped:
		value, err = decodeLogRecord[Reaped](data)
	case RecordKindCheckpoint:
		value, err = decodeLogRecord[Checkpoint](data)
	case RecordKindControlRequested:
		value, err = decodeLogRecord[ControlRequested](data)
	default:
		return nil, ErrInvalidLog
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidLog, err)
	}
	if !value.valid() {
		return nil, ErrInvalidLog
	}
	return value, nil
}

func decodeLogRecord[T Record](data []byte) (T, error) { return jsonwire.Decode[T](data) }

func recordProcess(record Record) ProcessID {
	switch value := record.(type) {
	case Started:
		return value.Process
	case SignalAdmitted:
		return value.Process
	case Stepped:
		return value.Process
	case EffectIntent:
		return value.ID.Process
	case EffectSettled:
		return value.ID.Process
	case Exited:
		return value.Process
	case ExecutionFailed:
		return value.Process
	case Reaped:
		return value.Process
	case ControlRequested:
		return value.Process
	case Checkpoint:
		return value.root()
	default:
		return ProcessID{}
	}
}

// Value objects already freeze their bytes. Only exported containers and
// pointers need copies, so reading long histories never re-encodes payloads.
func cloneRecord(record Record) (Record, error) {
	switch value := record.(type) {
	case Started, Activated, SignalAdmitted, Stepped, EffectIntent, Exited, Reaped, ControlRequested, ExecutionFailed:
		return value, nil
	case EffectSettled:
		if value.Failure != nil {
			value.Failure = new(*value.Failure)
		}
		return value, nil
	case Checkpoint:
		return value.clone(), nil
	default:
		return nil, ErrInvalidLog
	}
}
