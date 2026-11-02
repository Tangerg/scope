package run

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var (
	ErrInvalidLog           = errors.New("run: invalid log")
	ErrCommitConflict       = errors.New("run: log position conflict")
	ErrEpochConflict        = errors.New("run: writer epoch conflict")
	ErrNondeterministicStep = errors.New("run: replayed step differs from committed transition")
)

// Append is the only operation that advances a tree. An acknowledgment precedes
// publication and external dispatch. Load retains history for durable observers;
// recovery selects the newest Checkpoint and replays only its suffix.
type Store interface {
	Append(context.Context, Batch) error
	Load(context.Context, ProcessID) (Log, bool, error)
}

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
	return e.ID.Valid() && e.Outcome.Valid() && !e.At.IsZero() && (e.Failure == nil || e.Failure.Valid() && e.Outcome.Status() == SettlementStatusUnknown && string(e.Outcome.payload) == nullJSON)
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

type ControlRequested struct {
	Process ProcessID   `json:"process"`
	Kind    ControlKind `json:"kind"`
	Reason  string      `json:"reason"`
	At      time.Time   `json:"at"`
}

func (ControlRequested) recordKind() RecordKind { return RecordKindControlRequested }
func (c ControlRequested) valid() bool {
	return c.Process.Valid() && c.Kind.Valid() && !c.At.IsZero() && (c.Kind == ControlResume && c.Reason == "" || c.Kind != ControlResume && validateTerminationReason(c.Reason) == nil)
}

// Checkpoint is a materialization of acknowledged records. Only this record
// carries opaque ExecutionState; it cannot introduce new progress.
type Checkpoint struct {
	Processes        []ProcessCheckpoint `json:"processes"`
	StartedProcesses uint64              `json:"started_processes"`
}

func (Checkpoint) recordKind() RecordKind { return RecordKindCheckpoint }
func (c Checkpoint) valid() bool {
	if len(c.Processes) == 0 || c.StartedProcesses < uint64(len(c.Processes)) {
		return false
	}
	for _, process := range c.Processes {
		if !process.Started.valid() || !process.State.Valid() && process.Terminal == nil && process.Exited == nil {
			return false
		}
	}
	return true
}

func (c Checkpoint) clone() Checkpoint {
	c.Processes = slices.Clone(c.Processes)
	for index := range c.Processes {
		process := &c.Processes[index]
		process.Inbox = slices.Clone(process.Inbox)
		process.Receipts = slices.Clone(process.Receipts)
		process.Waits = slices.Clone(process.Waits)
		process.Pending = slices.Clone(process.Pending)
		for index := range process.Pending {
			pending := &process.Pending[index]
			if pending.Outcome != nil {
				pending.Outcome = new(*pending.Outcome)
			}
			if pending.Failure != nil {
				pending.Failure = new(*pending.Failure)
			}
		}
		if process.Terminal != nil {
			process.Terminal = new(*process.Terminal)
		}
		if process.Exited != nil {
			process.Exited = new(*process.Exited)
		}
	}
	return c
}

type ProcessCheckpoint struct {
	Started    Started             `json:"started"`
	State      ExecutionState      `json:"state,omitzero"`
	Step       uint64              `json:"step"`
	Inbox      Inbox               `json:"inbox"`
	Receipts   []SignalReceipt     `json:"receipts"`
	Waits      []Wait              `json:"waits"`
	Pending    []PendingCheckpoint `json:"pending"`
	Usage      Usage               `json:"usage"`
	Allocated  resourceAmounts     `json:"allocated"`
	ChildCount uint64              `json:"child_count"`
	Paused     bool                `json:"paused"`
	Yielded    bool                `json:"yielded"`
	Terminal   *Termination        `json:"terminal,omitzero"`
	Exited     *Exited             `json:"exited,omitzero"`
}

type SignalReceipt struct {
	ID     SignalID `json:"id"`
	Wait   WaitKey  `json:"wait,omitzero"`
	Digest Digest   `json:"digest"`
}

type PendingCheckpoint struct {
	Intent  EffectIntent `json:"intent"`
	Outcome *Settlement  `json:"outcome,omitzero"`
	Failure *Failure     `json:"failure,omitzero"`
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
func EncodeRecord(record Record) (string, []byte, error) {
	if err := validateRecord(record); err != nil {
		return "", nil, err
	}
	encoded, err := jsonv2.Marshal(record, jsonv2.Deterministic(true))
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrInvalidLog, err)
	}
	return string(record.recordKind()), encoded, nil
}
func DecodeRecord(kind string, data []byte) (Record, error) {
	var value Record
	var err error
	switch RecordKind(kind) {
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

type Entry struct {
	LSN    LSN
	Epoch  Epoch
	Record Record
}

func (e Entry) MarshalJSON() ([]byte, error) {
	kind, body, err := EncodeRecord(e.Record)
	if err != nil {
		return nil, err
	}
	return jsonv2.Marshal(entryWire{LSN: e.LSN, Epoch: e.Epoch, Kind: kind, Body: body})
}
func (e *Entry) UnmarshalJSON(data []byte) error {
	if e == nil {
		return ErrInvalidLog
	}
	wire, err := jsonwire.Decode[entryWire](data)
	if err != nil {
		return err
	}
	record, err := DecodeRecord(wire.Kind, wire.Body)
	if err != nil {
		return err
	}
	if wire.LSN == 0 {
		return ErrInvalidLog
	}
	*e = Entry{LSN: wire.LSN, Epoch: wire.Epoch, Record: record}
	return nil
}

type entryWire struct {
	LSN   LSN             `json:"lsn"`
	Epoch Epoch           `json:"epoch"`
	Kind  string          `json:"kind"`
	Body  json.RawMessage `json:"body"`
}

type Batch struct {
	Root    ProcessID
	Epoch   Epoch
	After   LSN
	Records []Record
}

func (b Batch) Validate() error {
	if !b.Root.Valid() || b.Root.Depth() != 0 || len(b.Records) == 0 || uint64(len(b.Records)) > math.MaxUint64-uint64(b.After) {
		return ErrInvalidLog
	}
	for _, record := range b.Records {
		if _, _, err := EncodeRecord(record); err != nil {
			return err
		}
		if process := recordProcess(record); process.Valid() && process.Root() != b.Root {
			return ErrInvalidLog
		}
	}
	if b.After == 0 {
		started, ok := b.Records[0].(Started)
		if !ok || started.Process != b.Root || b.Epoch != 0 || len(b.Records) < 2 {
			return ErrInvalidLog
		}
		activation, ok := b.Records[1].(Activated)
		if !ok || activation.Epoch != 1 {
			return ErrInvalidLog
		}
	}
	epoch := b.Epoch
	activations := 0
	for _, record := range b.Records {
		if activation, ok := record.(Activated); ok {
			if epoch == Epoch(math.MaxUint64) || activation.Epoch != epoch+1 {
				return ErrEpochConflict
			}
			epoch = activation.Epoch
			activations++
		}
	}
	if activations > 1 {
		return ErrInvalidLog
	}
	if activations != 0 && b.After != 0 && len(b.Records) != 1 {
		return ErrInvalidLog
	}
	return nil
}
func (b Batch) Entries() []Entry {
	entries := make([]Entry, len(b.Records))
	epoch := b.Epoch
	for index, record := range b.Records {
		if activation, ok := record.(Activated); ok {
			epoch = activation.Epoch
		}
		entries[index] = Entry{LSN: b.After + LSN(index) + 1, Epoch: epoch, Record: record}
	}
	return entries
}
func (b Batch) Admit(current Epoch, head LSN, existing []Entry) (Epoch, bool, error) {
	if err := b.Validate(); err != nil {
		return current, false, err
	}
	entries := b.Entries()
	next := entries[len(entries)-1].Epoch
	activationRetry := b.After < head && next == current && next == b.Epoch+1
	if b.Epoch != current && !activationRetry {
		return current, false, ErrEpochConflict
	}
	if b.After == head {
		if b.Epoch != current {
			return current, false, ErrEpochConflict
		}
		return next, false, nil
	}
	if b.After > head || len(existing) != len(entries) {
		return current, false, ErrCommitConflict
	}
	for index, entry := range entries {
		left, err := jsonv2.Marshal(entry)
		if err != nil {
			return current, false, err
		}
		right, err := jsonv2.Marshal(existing[index])
		if err != nil || string(left) != string(right) {
			return current, false, ErrCommitConflict
		}
	}
	return current, true, nil
}
func (b Batch) MarshalJSON() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return jsonv2.Marshal(batchWire{Root: b.Root, Epoch: b.Epoch, After: b.After, Entries: b.Entries()})
}
func (b *Batch) UnmarshalJSON(data []byte) error {
	if b == nil {
		return ErrInvalidLog
	}
	wire, err := jsonwire.Decode[batchWire](data)
	if err != nil {
		return err
	}
	value := Batch{Root: wire.Root, Epoch: wire.Epoch, After: wire.After, Records: make([]Record, len(wire.Entries))}
	for index, entry := range wire.Entries {
		value.Records[index] = entry.Record
	}
	if err := value.Validate(); err != nil {
		return err
	}
	for index, entry := range value.Entries() {
		if entry.LSN != wire.Entries[index].LSN || entry.Epoch != wire.Entries[index].Epoch {
			return ErrInvalidLog
		}
	}
	*b = value
	return nil
}

type batchWire struct {
	Root    ProcessID `json:"root"`
	Epoch   Epoch     `json:"epoch"`
	After   LSN       `json:"after"`
	Entries []Entry   `json:"entries"`
}

type Log struct {
	Root    ProcessID `json:"root"`
	Entries []Entry   `json:"entries"`
}

func (l Log) Head() LSN {
	if len(l.Entries) == 0 {
		return 0
	}
	return l.Entries[len(l.Entries)-1].LSN
}
func (l Log) Epoch() Epoch {
	if len(l.Entries) == 0 {
		return 0
	}
	return l.Entries[len(l.Entries)-1].Epoch
}
func (l Log) LatestCheckpoint() (Checkpoint, LSN, bool) {
	for index := len(l.Entries) - 1; index >= 0; index-- {
		if checkpoint, ok := l.Entries[index].Record.(Checkpoint); ok {
			return checkpoint.clone(), l.Entries[index].LSN, true
		}
	}
	return Checkpoint{}, 0, false
}
func (l Log) validateEnvelope() error {
	if !l.Root.Valid() || l.Root.Depth() != 0 || len(l.Entries) < 2 {
		return ErrInvalidLog
	}
	started, ok := l.Entries[0].Record.(Started)
	if !ok || started.Process != l.Root {
		return ErrInvalidLog
	}
	if activation, ok := l.Entries[1].Record.(Activated); !ok || activation.Epoch != 1 {
		return ErrInvalidLog
	}
	epoch := Epoch(0)
	for index, entry := range l.Entries {
		if entry.LSN != LSN(index)+1 {
			return ErrInvalidLog
		}
		if err := validateRecord(entry.Record); err != nil {
			return err
		}
		_, checkpoint := entry.Record.(Checkpoint)
		if started.Limits.MaxRecordBytes.limited || checkpoint && started.Limits.MaxCheckpointBytes.limited {
			_, encoded, err := EncodeRecord(entry.Record)
			if err != nil {
				return err
			}
			if !started.Limits.MaxRecordBytes.Allows(uint64(len(encoded))) || checkpoint && !started.Limits.MaxCheckpointBytes.Allows(uint64(len(encoded))) {
				return ErrResourceLimitExceeded
			}
		}
		if process := recordProcess(entry.Record); process.Valid() && process.Root() != l.Root {
			return ErrInvalidLog
		}
		if activation, ok := entry.Record.(Activated); ok {
			if epoch == Epoch(math.MaxUint64) || activation.Epoch != epoch+1 {
				return ErrInvalidLog
			}
			epoch = activation.Epoch
		}
		if entry.Epoch != epoch {
			return ErrInvalidLog
		}
	}
	if epoch == 0 {
		return ErrInvalidLog
	}
	return nil
}
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
	default:
		return ProcessID{}
	}
}

func (b Batch) Clone() (Batch, error) {
	if err := b.Validate(); err != nil {
		return Batch{}, err
	}
	owned := b
	owned.Records = make([]Record, len(b.Records))
	for index, record := range b.Records {
		copy, err := cloneRecord(record)
		if err != nil {
			return Batch{}, err
		}
		owned.Records[index] = copy
	}
	return owned, nil
}

func (l Log) Clone() (Log, error) {
	if err := l.Validate(); err != nil {
		return Log{}, err
	}
	entries, err := cloneEntries(l.Entries)
	if err != nil {
		return Log{}, err
	}
	return Log{Root: l.Root, Entries: entries}, nil
}
func (l Log) Inspect() (TreeInspection, error) {
	state, err := projectLog(l)
	if err != nil {
		return TreeInspection{}, err
	}
	view := state.inspect()
	view.receipts = make(map[ProcessID][]SignalReceipt)
	for _, entry := range l.Entries {
		if admitted, ok := entry.Record.(SignalAdmitted); ok {
			view.receipts[admitted.Process] = append(view.receipts[admitted.Process], SignalReceipt{ID: admitted.Signal.ID, Wait: admitted.Signal.Wait, Digest: admitted.Signal.digest()})
		}
	}
	return view, nil
}

func (l Log) Validate() error { _, err := projectLog(l); return err }

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
