package run

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"math"
	"slices"
	"strings"
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

// Store owns one append-only log per root tree. Recovery and history are
// separate reads so neither pays for the other's range: Load skips the prefix
// covered by the newest checkpoint, and Read is incremental.
type Store interface {
	// Append is the only operation that advances a tree. It must atomically
	// compare the writer Epoch and the head position before extending the log,
	// and acknowledge a byte-identical retry of an applied batch.
	Append(ctx context.Context, batch Batch) error
	// Load returns the recovery segment: the newest Checkpoint entry and every
	// entry after it, or the whole log when it holds no Checkpoint.
	Load(ctx context.Context, root ProcessID) (Log, bool, error)
	// Read yields the committed entries after position in order. It reads one
	// consistent snapshot and ends at that snapshot's head.
	Read(ctx context.Context, root ProcessID, after LSN) iter.Seq2[Entry, error]
}

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
	return e.ID.Valid() && e.Outcome.Valid() && !e.At.IsZero() && (e.Failure == nil || e.Failure.Valid() && e.Outcome.Status() == SettlementStatusUnknown && e.Outcome.payload.equal(nullPayload))
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

// Checkpoint materializes every acknowledged record before it. Recovery starts
// from the newest one without reading its prefix, so it is the authority for
// that prefix rather than a cache to be checked against it; it can only be
// written by the fenced writer that folded the prefix. Only this record carries
// opaque ExecutionState.
type Checkpoint struct {
	Processes        []ProcessCheckpoint `json:"processes"`
	StartedProcesses uint64              `json:"started_processes"`
}

func (Checkpoint) recordKind() RecordKind { return RecordKindCheckpoint }
func (c Checkpoint) valid() bool {
	if len(c.Processes) == 0 || c.StartedProcesses < uint64(len(c.Processes)) {
		return false
	}
	for index, process := range c.Processes {
		if !process.valid() || index > 0 && c.Processes[index-1].Started.Process.compare(process.Started.Process) >= 0 {
			return false
		}
	}
	return true
}

// tree restores the materialization recorded at entry. Child counters are
// derived from the captured membership instead of being stored a second time.
func (c Checkpoint) tree(root ProcessID, entry Entry) (*treeState, error) {
	state := newTreeState(root)
	state.head, state.epoch, state.startedProcesses = entry.LSN, entry.Epoch, c.StartedProcesses
	for _, captured := range c.Processes {
		id := captured.Started.Process
		if id.Root() != root || state.processes[id] != nil {
			return nil, ErrInvalidLog
		}
		state.processes[id] = captured.process()
	}
	if err := state.restoreFromCheckpoint(entry.LSN); err != nil {
		return nil, err
	}
	return state, nil
}

func (c Checkpoint) clone() Checkpoint {
	c.Processes = slices.Clone(c.Processes)
	for index := range c.Processes {
		c.Processes[index] = c.Processes[index].clone()
	}
	return c
}

// StartedChild records where a child's Started entry sits in the log.
type StartedChild struct {
	Key     ChildKey `json:"key"`
	Started LSN      `json:"started"`
}

type ProcessCheckpoint struct {
	Started   Started             `json:"started"`
	State     ExecutionState      `json:"state,omitzero"`
	Inbox     Inbox               `json:"inbox"`
	Receipts  []SignalReceipt     `json:"receipts"`
	Children  []StartedChild      `json:"children"`
	Waits     []Wait              `json:"waits"`
	Pending   []PendingCheckpoint `json:"pending"`
	Usage     Usage               `json:"usage"`
	Allocated resourceAmounts     `json:"allocated"`
	Paused    bool                `json:"paused"`
	Yielded   bool                `json:"yielded"`
	Terminal  *Termination        `json:"terminal,omitzero"`
	Exited    *Exited             `json:"exited,omitzero"`
}

// A live process is exactly one that still owns ExecutionState; an exit must
// repeat the terminal intent it completed.
func (p ProcessCheckpoint) valid() bool {
	if !p.Started.valid() || p.State.Valid() != (p.Terminal == nil) || !p.childrenValid() || !p.waitsAndReceiptsValid() || !p.pendingValid() || !p.inboxValid() || !p.resourcesValid() {
		return false
	}
	if p.Terminal != nil && (!p.Terminal.Valid() || len(p.Waits) != 0 || p.Paused || p.Yielded) {
		return false
	}
	return p.Exited == nil || p.Terminal != nil && p.Exited.valid() && p.Exited.Process == p.Started.Process && p.Terminal.equal(p.Exited.Termination)
}

func (p ProcessCheckpoint) waitsAndReceiptsValid() bool {
	answered := make(map[WaitKey]struct{})
	for index, receipt := range p.Receipts {
		if !receipt.ID.Valid() || !receipt.Digest.Valid() || index > 0 && p.Receipts[index-1].ID.String() >= receipt.ID.String() {
			return false
		}
		if receipt.Wait.Valid() {
			if _, duplicate := answered[receipt.Wait]; duplicate {
				return false
			}
			answered[receipt.Wait] = struct{}{}
		}
	}
	for index, wait := range p.Waits {
		if !wait.Valid() || index > 0 && p.Waits[index-1].Key.String() >= wait.Key.String() {
			return false
		}
		if _, exists := answered[wait.Key]; exists {
			return false
		}
	}
	return true
}

func (p ProcessCheckpoint) pendingValid() bool {
	for index, pending := range p.Pending {
		id := pending.Intent.ID
		if !pending.valid() || id.Process != p.Started.Process || id.Step > p.Usage.CommittedSteps || uint64(id.Index) >= p.Usage.PreparedEffects || !p.Started.Capabilities.Allows(pending.Intent.Policy.RequiredCapabilities) || index > 0 && p.Pending[index-1].Intent.ID.compare(id) >= 0 {
			return false
		}
	}
	return uint64(len(p.Pending)) <= p.Usage.PreparedEffects
}

func (p ProcessCheckpoint) inboxValid() bool {
	signals := make(map[SignalID]struct{})
	effects := make(map[EffectID]struct{})
	children := make(map[ProcessID]struct{})
	for _, message := range p.Inbox {
		switch value := message.(type) {
		case Signal:
			index, found := slices.BinarySearchFunc(p.Receipts, value.ID, func(receipt SignalReceipt, id SignalID) int { return strings.Compare(receipt.ID.String(), id.String()) })
			if !value.Valid() || !found || p.Receipts[index] != value.receipt() {
				return false
			}
			if _, duplicate := signals[value.ID]; duplicate {
				return false
			}
			signals[value.ID] = struct{}{}
		case Completion:
			if !value.Valid() || value.Effect.Process != p.Started.Process || value.Effect.Step > p.Usage.CommittedSteps || uint64(value.Effect.Index) >= p.Usage.PreparedEffects {
				return false
			}
			if _, duplicate := effects[value.Effect]; duplicate {
				return false
			}
			if _, pending := slices.BinarySearchFunc(p.Pending, value.Effect, func(pending PendingCheckpoint, id EffectID) int { return pending.Intent.ID.compare(id) }); pending {
				return false
			}
			effects[value.Effect] = struct{}{}
		case Exit:
			parent, child := value.Child.Parent()
			if !value.Valid() || !child || parent != p.Started.Process {
				return false
			}
			if _, duplicate := children[value.Child]; duplicate {
				return false
			}
			children[value.Child] = struct{}{}
		default:
			return false
		}
	}
	return resourceQuantitiesFit(p.Usage.AcceptedMessages, uint64(len(p.Receipts)), uint64(len(effects)), uint64(len(children))) && resourceQuantitiesFit(p.Usage.PreparedEffects, uint64(len(p.Pending)), uint64(len(effects)))
}

func (p ProcessCheckpoint) resourcesValid() bool {
	budget, usage, allocated := p.Started.Budget, p.Usage, p.Allocated
	if !budget.Steps.limited && allocated.Steps != 0 || !budget.Effects.limited && allocated.Effects != 0 || !budget.Messages.limited && allocated.Messages != 0 {
		return false
	}
	children := uint64(len(p.Children))
	return budget.Steps.Allows(usage.CommittedSteps, allocated.Steps) && budget.Effects.Allows(usage.PreparedEffects, allocated.Effects) && budget.Messages.Allows(usage.AcceptedMessages, allocated.Messages) && (usage.CommittedSteps != 0 || usage.PreparedEffects == 0) && usage.PreparedEffects >= children && usage.AcceptedMessages >= children
}

// childrenValid requires the canonical key order the materialization writes,
// which also rules out a key naming two children.
func (p ProcessCheckpoint) childrenValid() bool {
	for index, child := range p.Children {
		if !child.Key.Valid() || child.Started == 0 || index > 0 && p.Children[index-1].Key.String() >= child.Key.String() {
			return false
		}
	}
	return true
}

func (p ProcessCheckpoint) process() *processState {
	process := newProcessState(p.Started)
	process.inbox, process.usage, process.allocated = slices.Clone(p.Inbox), p.Usage, p.Allocated
	process.paused, process.yielded = p.Paused, p.Yielded
	for _, receipt := range p.Receipts {
		process.receipts[receipt.ID] = receipt
	}
	for _, child := range p.Children {
		process.children[child.Key] = child.Started
	}
	for _, wait := range p.Waits {
		process.waits[wait.Key] = wait
	}
	for _, pending := range p.Pending {
		process.pending[pending.Intent.ID] = pending.clone()
	}
	if p.Terminal != nil {
		process.terminal = *p.Terminal
	}
	if p.Exited != nil {
		process.exited = new(*p.Exited)
		close(process.done)
	}
	return process
}

func (p ProcessCheckpoint) clone() ProcessCheckpoint {
	p.Inbox = slices.Clone(p.Inbox)
	p.Receipts = slices.Clone(p.Receipts)
	p.Children = slices.Clone(p.Children)
	p.Waits = slices.Clone(p.Waits)
	p.Pending = slices.Clone(p.Pending)
	for index := range p.Pending {
		p.Pending[index] = p.Pending[index].clone()
	}
	if p.Terminal != nil {
		p.Terminal = new(*p.Terminal)
	}
	if p.Exited != nil {
		p.Exited = new(*p.Exited)
	}
	return p
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

func (p PendingCheckpoint) valid() bool {
	if !p.Intent.valid() {
		return false
	}
	if p.Outcome == nil {
		return p.Failure == nil
	}
	return p.Intent.Effect.Target() == EffectTargetDispatcher && p.Outcome.Status() == SettlementStatusUnknown && (EffectSettled{ID: p.Intent.ID, Outcome: *p.Outcome, Failure: p.Failure, At: p.Intent.At}).valid()
}

func (p PendingCheckpoint) inspect(epoch Epoch, program ProgramRef) PendingEffect {
	effect := PendingEffect{Request: EffectRequest{id: p.Intent.ID, epoch: epoch, program: program, effect: p.Intent.Effect}, Policy: p.Intent.Policy}
	if p.Failure != nil {
		effect.Failure = new(*p.Failure)
	}
	if p.Outcome != nil {
		effect.Outcome = *p.Outcome
	}
	return effect
}

func (p PendingCheckpoint) clone() PendingCheckpoint {
	if p.Outcome != nil {
		p.Outcome = new(*p.Outcome)
	}
	if p.Failure != nil {
		p.Failure = new(*p.Failure)
	}
	return p
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

// Entry places a Record at its position and under the Epoch that wrote it.
type Entry struct {
	LSN    LSN
	Epoch  Epoch
	Record Record
}

func (e Entry) belongsTo(root ProcessID) bool {
	if validateRecord(e.Record) != nil {
		return false
	}
	process := recordProcess(e.Record)
	return !process.Valid() || process.Root() == root
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
	Kind  RecordKind      `json:"kind"`
	Body  json.RawMessage `json:"body"`
}

// Batch is the unit a Store appends atomically: Records placed after After by
// the writer holding Epoch. Entries derives each record's coordinates, so a
// Store never assigns positions itself.
type Batch struct {
	Root    ProcessID
	Epoch   Epoch
	After   LSN
	Records []Record
}

// admit checks the batch and bounds each record by the tree's limits; the batch
// that creates a tree carries the limits it will be bound by.
func (b Batch) admit(limits TreeLimits) error {
	if err := b.Validate(); err != nil {
		return err
	}
	if started, creates := b.Records[0].(Started); creates && b.After == 0 {
		limits = started.Limits
	}
	for _, record := range b.Records {
		if err := limits.admitRecord(record); err != nil {
			return err
		}
	}
	return nil
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

// Admit is the one append decision every Store makes against its current
// Epoch and head, given the stored entries the batch would occupy. It returns
// the Epoch after the batch, or retry=true when an identical batch is already
// stored and the append must be acknowledged without writing.
func (b Batch) Admit(current Epoch, head LSN, existing []Entry) (next Epoch, retry bool, err error) {
	if err := b.Validate(); err != nil {
		return current, false, err
	}
	entries := b.Entries()
	next = entries[len(entries)-1].Epoch
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
		left, encodeErr := jsonv2.Marshal(entry)
		if encodeErr != nil {
			return current, false, encodeErr
		}
		right, encodeErr := jsonv2.Marshal(existing[index])
		if encodeErr != nil || string(left) != string(right) {
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

// Log is a contiguous segment of one tree's log that begins at the tree's
// Start or at a Checkpoint, so it can be folded without what precedes it.
type Log struct {
	Root    ProcessID `json:"root"`
	Entries []Entry   `json:"entries"`
}

// Complete reports whether the log begins at the tree's Start rather than at a
// Checkpoint, which history projections require.
func (l Log) Complete() bool { return len(l.Entries) > 0 && l.Entries[0].LSN == 1 }

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

// rootStarted returns the root's Start from the segment's leading record.
func (l Log) rootStarted() (Started, error) {
	if len(l.Entries) == 0 {
		return Started{}, ErrInvalidLog
	}
	switch first := l.Entries[0].Record.(type) {
	case Started:
		if first.Process == l.Root {
			return first, nil
		}
	case Checkpoint:
		for _, process := range first.Processes {
			if process.Started.Process == l.Root {
				return process.Started, nil
			}
		}
	}
	return Started{}, ErrInvalidLog
}

// base is the state a Log starts from: the tree its leading Checkpoint
// materialized, or an empty tree when the Log begins with the tree's Start.
func (l Log) base() (*treeState, []Entry, error) {
	if err := l.validateEnvelope(); err != nil {
		return nil, nil, err
	}
	first := l.Entries[0]
	if checkpoint, ok := first.Record.(Checkpoint); ok {
		state, err := checkpoint.tree(l.Root, first)
		return state, l.Entries[1:], err
	}
	return newTreeState(l.Root), l.Entries, nil
}

// validateEnvelope checks the coordinates every segment shares: contiguous
// positions, monotonic epochs, one root, and resolved record limits. A segment
// begins either at the tree's Start or at a Checkpoint.
func (l Log) validateEnvelope() error {
	if !l.Root.Valid() || l.Root.Depth() != 0 || len(l.Entries) == 0 {
		return ErrInvalidLog
	}
	limits, epoch, err := l.origin()
	if err != nil {
		return err
	}
	first := l.Entries[0].LSN
	for index, entry := range l.Entries {
		if entry.LSN != first+LSN(index) {
			return ErrInvalidLog
		}
		if err := limits.admitRecord(entry.Record); err != nil {
			return err
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
	return nil
}

// origin returns the limits and writer epoch in force before the first entry.
func (l Log) origin() (TreeLimits, Epoch, error) {
	started, err := l.rootStarted()
	if err != nil {
		return TreeLimits{}, 0, err
	}
	first := l.Entries[0]
	if _, checkpoint := first.Record.(Checkpoint); checkpoint {
		if first.Epoch == 0 {
			return TreeLimits{}, 0, ErrInvalidLog
		}
		return started.Limits, first.Epoch, nil
	}
	if first.LSN != 1 || len(l.Entries) < 2 {
		return TreeLimits{}, 0, ErrInvalidLog
	}
	if activation, ok := l.Entries[1].Record.(Activated); !ok || activation.Epoch != 1 {
		return TreeLimits{}, 0, ErrInvalidLog
	}
	return started.Limits, 0, nil
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
	return state.inspect(), nil
}

// Usage requires the complete prefix because checkpoints omit reaped
// processes. A recovery cut cannot certify the tree's historical total.
func (l Log) Usage() (Usage, error) {
	if err := l.Validate(); err != nil {
		return Usage{}, err
	}
	if !l.Complete() {
		return Usage{}, fmt.Errorf("%w: usage requires history from the tree start", ErrInvalidLog)
	}
	var usage Usage
	for _, entry := range l.Entries {
		_, increment := recordUsage(entry.Record)
		var err error
		usage, err = usage.add(increment)
		if err != nil {
			return Usage{}, err
		}
	}
	return usage, nil
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
