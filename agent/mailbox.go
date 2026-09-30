package agent

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/samber/lo"
)

var (
	ErrSignalRejected = errors.New("agent: signal rejected")
	// ErrSignalConflict reports a repeated SignalID within one batch or reuse
	// of an accepted identity with different immutable content or WaitID.
	ErrSignalConflict = errors.New("agent: conflicting signal identity")
	errMailboxCursor  = errors.New("agent: invalid signal cursor")
	errWaitState      = errors.New("agent: invalid wait state")
)

type signalRecord struct {
	arrivalSequence uint64
	id              SignalID
	waitID          WaitID
	payloadDigest   Digest
	payload         json.RawMessage
	opensWait       bool
	source          signalSource
}

func newSignalRecord(signal Signal, opensWait bool) signalRecord {
	return signalRecord{
		id: signal.id, waitID: signal.waitID, payload: signal.payload,
		payloadDigest: ComputeDigest(signal.payload), opensWait: opensWait, source: signalSourceExternal,
	}
}

func newAdmissionRecord(signal Signal, source signalSource) (signalRecord, error) {
	if !signal.Valid() || !source.accepts(signal.ID()) {
		return signalRecord{}, fmt.Errorf("%w: %w", ErrSignalRejected, ErrInvalidSignal)
	}
	record := newSignalRecord(signal, false)
	record.source = source
	return record, nil
}

func (s signalRecord) sameContent(other signalRecord) bool {
	return s.id == other.id && s.waitID == other.waitID && s.payloadDigest == other.payloadDigest && s.opensWait == other.opensWait
}

func (s signalRecord) wire() signalRecordWire {
	wire := signalRecordWire{
		ArrivalSequence: s.arrivalSequence, ID: s.id,
		PayloadDigest: s.payloadDigest, Payload: bytes.Clone(s.payload), OpensWait: s.opensWait, Source: s.source,
	}
	if s.waitID.Valid() {
		wire.WaitID = &s.waitID
	}
	return wire
}

type waitRecord struct {
	key      WaitKey
	id       WaitID
	kind     WaitKind
	answered bool
	closed   bool
}

// Only Host input answers an external wait; only the Engine answers a child wait.
func (w waitRecord) answerableBy(source signalSource) bool {
	return (w.kind == WaitKindExternal) == (source == signalSourceExternal)
}

// signalMailbox has reference semantics. candidate must clone it before any
// speculative mutation: commit may advance a prefix before returning an error,
// and that candidate must then be discarded in full.
type signalMailbox struct {
	records      []signalRecord
	seen         map[SignalID]int
	waits        map[WaitID]waitRecord
	signalCursor uint64
}

func (s *signalMailbox) clone() signalMailbox {
	clone := signalMailbox{
		records: slices.Clone(s.records), seen: maps.Clone(s.seen),
		waits: maps.Clone(s.waits), signalCursor: s.signalCursor,
	}
	return clone
}

func newSignalMailbox() signalMailbox {
	return signalMailbox{
		seen:  make(map[SignalID]int),
		waits: make(map[WaitID]waitRecord),
	}
}

type signalSource string

const (
	signalSourceExternal   signalSource = "external"
	signalSourceChildWait  signalSource = "child_wait"
	signalSourceSettlement signalSource = "settlement"
)

func (s signalSource) accepts(id SignalID) bool {
	switch s {
	case signalSourceExternal:
		return id.Valid() && !id.engineOwned()
	case signalSourceSettlement, signalSourceChildWait:
		return id.Valid() && id.engineOwned()
	default:
		return false
	}
}

func (s *signalMailbox) enqueue(status Status, signal Signal, source signalSource) (bool, error) {
	record, err := newAdmissionRecord(signal, source)
	if err != nil {
		return false, err
	}
	return s.enqueueRecord(status, record)
}

func (s *signalMailbox) enqueueRecord(status Status, record signalRecord) (bool, error) {
	accepted, err := s.validateRecord(status, record)
	if err != nil || !accepted {
		return accepted, err
	}
	s.acceptRecord(record)
	return true, nil
}

// validateRecord reports false without error for an identical duplicate.
func (s *signalMailbox) validateRecord(status Status, record signalRecord) (bool, error) {
	if index, exists := s.seen[record.id]; exists {
		return false, s.validateDuplicate(s.records[index], record)
	}
	if !status.acceptsSignals() {
		return false, ErrSignalRejected
	}
	if !record.waitID.Valid() {
		if record.source == signalSourceChildWait {
			return false, ErrSignalRejected
		}
		return true, nil
	}
	wait, exists := s.waits[record.waitID]
	if !exists || !wait.answerableBy(record.source) || wait.closed || wait.answered {
		return false, ErrSignalRejected
	}
	return true, nil
}

func (s *signalMailbox) validateDuplicate(existing, record signalRecord) error {
	if !existing.sameContent(record) {
		return ErrSignalConflict
	}
	if existing.source != record.source ||
		record.waitID.Valid() && !s.waits[record.waitID].answerableBy(record.source) {
		return ErrSignalRejected
	}
	return nil
}

func (s *signalMailbox) acceptRecord(record signalRecord) {
	if record.waitID.Valid() {
		wait := s.waits[record.waitID]
		wait.answered = true
		s.waits[record.waitID] = wait
	}
	s.appendRecord(record)
}

func (s *signalMailbox) openWait(key WaitKey, signal Signal, kind WaitKind) error {
	_, addressed := signal.WaitID()
	if !key.Valid() || !signal.Valid() || !addressed {
		return fmt.Errorf("%w: wait key and addressed opening Signal are required", errWaitState)
	}
	record := newSignalRecord(signal, true)
	record.source = signalSourceSettlement
	return s.openWaitRecord(key, record, kind)
}

func (s *signalMailbox) openWaitRecord(key WaitKey, record signalRecord, kind WaitKind) error {
	id := record.waitID
	if !kind.Valid() {
		return fmt.Errorf("%w: unknown wait kind %q", errWaitState, kind)
	}
	if !signalSourceSettlement.accepts(record.id) {
		return fmt.Errorf("%w: opening Signal requires Engine identity", errWaitState)
	}
	if _, exists := s.waits[id]; exists {
		return fmt.Errorf("%w: duplicate wait ID", errWaitState)
	}
	if s.contains(record.id) {
		return fmt.Errorf("%w: duplicate opening SignalID", errWaitState)
	}
	for _, wait := range s.waits {
		if wait.key == key && !wait.closed {
			return fmt.Errorf("%w: wait key is already open", errWaitState)
		}
	}
	s.waits[id] = waitRecord{key: key, id: id, kind: kind}
	s.appendRecord(record)
	return nil
}

func (s *signalMailbox) appendRecord(record signalRecord) {
	// seen stores zero-based indexes; persisted arrival sequences start at one.
	s.seen[record.id] = len(s.records)
	record.arrivalSequence = uint64(len(s.records)) + 1
	s.records = append(s.records, record)
}

func (s *signalMailbox) enterWait(id WaitID) (bool, error) {
	record, exists := s.waits[id]
	if !exists || record.closed {
		return false, errWaitState
	}
	return !record.answered, nil
}

func (s *signalMailbox) closeWait(id WaitID) error {
	record, exists := s.waits[id]
	if !exists || record.closed {
		return errWaitState
	}
	record.closed = true
	s.waits[id] = record
	return nil
}

// closeAllWaits makes every remaining wait terminal with its Process. It
// returns child-wait identities whose Engine registrations must be removed
// before the terminal ProcessSnapshot is captured.
func (s *signalMailbox) closeAllWaits() []WaitID {
	var childWaits []WaitID
	for id, record := range s.waits {
		if record.closed {
			continue
		}
		record.closed = true
		s.waits[id] = record
		if record.kind == WaitKindChildren {
			childWaits = append(childWaits, id)
		}
	}
	slices.SortFunc(childWaits, func(left, right WaitID) int {
		return cmp.Compare(left.String(), right.String())
	})
	return childWaits
}

func (s *signalMailbox) pending() []Signal {
	if s.signalCursor >= uint64(len(s.records)) {
		return nil
	}
	pending := s.records[s.signalCursor:]
	signals := make([]Signal, len(pending))
	for index := range pending {
		record := pending[index]
		// Admission already normalized these immutable, mailbox-owned bytes.
		signals[index] = Signal{id: record.id, waitID: record.waitID, payload: record.payload}
	}
	return signals
}

func (s *signalMailbox) commit(consumedSignals uint32) ([]WaitID, error) {
	remaining := uint64(len(s.records)) - s.signalCursor
	if uint64(consumedSignals) > remaining {
		return nil, errMailboxCursor
	}
	var childWaits []WaitID
	for index := s.signalCursor; index < s.signalCursor+uint64(consumedSignals); index++ {
		record := &s.records[index]
		if waitID := record.waitID; waitID.Valid() && !record.opensWait {
			if err := s.closeWait(waitID); err != nil {
				return nil, err
			}
			if s.waits[waitID].kind == WaitKindChildren {
				childWaits = append(childWaits, waitID)
			}
		}
		// Candidate adoption owns consumption; history only needs identity,
		// content agreement, and wait facts after that boundary.
		record.payload = nil
	}
	s.signalCursor += uint64(consumedSignals)
	return childWaits, nil
}

func (s *signalMailbox) acceptedCount() uint64 { return uint64(len(s.records)) }

func (s *signalMailbox) committedSignalCursor() uint64 { return s.signalCursor }

func (s *signalMailbox) pendingCount() uint64 {
	return s.acceptedCount() - s.signalCursor
}

func (s *signalMailbox) contains(id SignalID) bool {
	_, exists := s.seen[id]
	return exists
}

type signalRecordWire struct {
	ArrivalSequence uint64          `json:"arrival_sequence"`
	ID              SignalID        `json:"id"`
	WaitID          *WaitID         `json:"wait_id,omitzero"`
	PayloadDigest   Digest          `json:"payload_digest"`
	Payload         json.RawMessage `json:"payload,omitzero"`
	OpensWait       bool            `json:"opens_wait,omitzero"`
	Source          signalSource    `json:"source"`
}

type waitRecordWire struct {
	WaitKey  WaitKey  `json:"wait_key"`
	WaitID   WaitID   `json:"wait_id"`
	Kind     WaitKind `json:"kind"`
	Answered bool     `json:"answered"`
	Closed   bool     `json:"closed"`
}

type mailboxWire struct {
	Signals      []signalRecordWire `json:"signals,omitempty"`
	SignalCursor uint64             `json:"signal_cursor"`
	Waits        []waitRecordWire   `json:"waits,omitempty"`
}

func (m mailboxWire) receipts() []SignalReceipt {
	receipts := make([]SignalReceipt, 0, len(m.Signals))
	for _, record := range m.Signals {
		receipt := SignalReceipt{
			id: record.ID, waitID: lo.FromPtr(record.WaitID), payloadDigest: record.PayloadDigest,
			arrivalSequence: record.ArrivalSequence, consumed: record.ArrivalSequence <= m.SignalCursor,
			external: record.Source == signalSourceExternal,
		}
		if !receipt.consumed {
			receipt.pending = Signal{id: receipt.id, waitID: receipt.waitID, payload: record.Payload}
		}
		receipts = append(receipts, receipt)
	}
	return receipts
}

func (m mailboxWire) openChildWaits() map[WaitID]*childWaitValidationFacts {
	waits := make(map[WaitID]*childWaitValidationFacts)
	for _, record := range m.Waits {
		if record.Kind == WaitKindChildren && !record.Closed {
			waits[record.WaitID] = &childWaitValidationFacts{record: record}
		}
	}
	for _, signal := range m.Signals {
		if signal.WaitID == nil {
			continue
		}
		if facts := waits[*signal.WaitID]; facts != nil {
			facts.signals = append(facts.signals, signal)
		}
	}
	return waits
}

func (m mailboxWire) waitRecord(id WaitID) (waitRecordWire, bool) {
	for _, record := range m.Waits {
		if record.WaitID == id {
			return record, true
		}
	}
	return waitRecordWire{}, false
}

func (s *signalMailbox) wire() mailboxWire {
	wire := mailboxWire{SignalCursor: s.signalCursor}
	for _, record := range s.records {
		wire.Signals = append(wire.Signals, record.wire())
	}
	for _, record := range s.waits {
		wire.Waits = append(wire.Waits, waitRecordWire{
			WaitKey: record.key, WaitID: record.id, Kind: record.kind,
			Answered: record.answered, Closed: record.closed,
		})
	}
	slices.SortFunc(wire.Waits, func(left, right waitRecordWire) int {
		return cmp.Compare(left.WaitID.String(), right.WaitID.String())
	})
	return wire
}

// Admission validates the whole batch against history before returning new records.
func (s *signalMailbox) prepareAdmission(status Status, currentWaitID WaitID, signals []Signal, source signalSource) ([]signalRecord, error) {
	records := make([]signalRecord, 0, len(signals))
	seen := make(map[SignalID]struct{}, len(signals))
	answered := make(map[WaitID]struct{})
	for _, signal := range signals {
		record, err := newAdmissionRecord(signal, source)
		if err != nil {
			return nil, err
		}
		if _, found := seen[record.id]; found {
			return nil, ErrSignalConflict
		}
		seen[record.id] = struct{}{}
		accepted, err := s.validateRecord(status, record)
		if err != nil {
			return nil, err
		}
		if !accepted {
			// A duplicate does not excuse a conflict or unauthorized address later
			// in the batch. Nothing is applied unless every entry passes preflight.
			continue
		}
		if waitID := record.waitID; waitID.Valid() {
			if _, alreadyAnswered := answered[waitID]; alreadyAnswered {
				return nil, ErrSignalRejected
			}
			answered[waitID] = struct{}{}
		}
		if source == signalSourceExternal && s.blockedByCurrentWait(currentWaitID, record.waitID, answered) {
			return nil, ErrSignalRejected
		}
		records = append(records, record)
	}
	return records, nil
}

// A current wait rejects answers to any other wait. A current external wait
// also rejects unaddressed input until the batch has answered it.
func (s *signalMailbox) blockedByCurrentWait(currentWaitID, waitID WaitID, answered map[WaitID]struct{}) bool {
	if !currentWaitID.Valid() || waitID == currentWaitID {
		return false
	}
	if waitID.Valid() {
		return true
	}
	_, currentAnswered := answered[currentWaitID]
	return s.waits[currentWaitID].kind == WaitKindExternal && !currentAnswered
}

// Restoration replays portable facts through the live mailbox transitions.
// Only final Process termination can close an unanswered or unconsumed wait.
func restoreSignalMailbox(wire mailboxWire, status Status) (signalMailbox, error) {
	if wire.SignalCursor > uint64(len(wire.Signals)) {
		return signalMailbox{}, errMailboxCursor
	}
	waits, err := wire.waitIndex()
	if err != nil {
		return signalMailbox{}, err
	}
	mailbox := newSignalMailbox()
	for index, encoded := range wire.Signals {
		record, err := encoded.restore(uint64(index+1), wire.SignalCursor)
		if err != nil {
			return signalMailbox{}, err
		}
		if err := mailbox.replay(record, waits); err != nil {
			return signalMailbox{}, err
		}
		if record.arrivalSequence <= wire.SignalCursor {
			if _, err := mailbox.commit(1); err != nil {
				return signalMailbox{}, err
			}
		}
	}
	if status.Terminal() {
		mailbox.closeAllWaits()
	}
	if err := mailbox.matchWaits(waits); err != nil {
		return signalMailbox{}, err
	}
	return mailbox, nil
}

func (m mailboxWire) waitIndex() (map[WaitID]waitRecordWire, error) {
	waits := make(map[WaitID]waitRecordWire, len(m.Waits))
	for _, record := range m.Waits {
		if !record.WaitKey.Valid() || !record.WaitID.Valid() {
			return nil, errWaitState
		}
		if _, duplicate := waits[record.WaitID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate WaitID", errWaitState)
		}
		waits[record.WaitID] = record
	}
	return waits, nil
}

func (s *signalMailbox) replay(record signalRecord, waits map[WaitID]waitRecordWire) error {
	if !record.opensWait {
		accepted, err := s.enqueueRecord(StatusRunning, record)
		if err != nil || !accepted {
			return errors.Join(err, errors.New("invalid mailbox Signal history"))
		}
		return nil
	}
	wait, exists := waits[record.waitID]
	if !exists {
		return fmt.Errorf("%w: opening Signal has no wait", errWaitState)
	}
	return s.openWaitRecord(wait.WaitKey, record, wait.Kind)
}

func (s *signalMailbox) matchWaits(expected map[WaitID]waitRecordWire) error {
	if len(s.waits) != len(expected) {
		return fmt.Errorf("%w: wait has no opening Signal", errWaitState)
	}
	for id, want := range expected {
		actual := s.waits[id]
		if actual.answered != want.Answered || actual.closed != want.Closed {
			return fmt.Errorf("%w: wait lifecycle disagrees with Signal history", errWaitState)
		}
	}
	return nil
}

func (s signalRecordWire) restore(sequence, cursor uint64) (signalRecord, error) {
	if err := s.validateShape(sequence); err != nil {
		return signalRecord{}, err
	}
	record := signalRecord{
		arrivalSequence: sequence, id: s.ID, waitID: lo.FromPtr(s.WaitID),
		payloadDigest: s.PayloadDigest, opensWait: s.OpensWait, source: s.Source,
	}
	if sequence <= cursor {
		if len(s.Payload) != 0 {
			return signalRecord{}, fmt.Errorf("%w: consumed Signal retains payload", errMailboxCursor)
		}
		return record, nil
	}
	payload, err := normalizeJSON(s.Payload, MaxPayloadBytes)
	if err != nil || ComputeDigest(payload) != s.PayloadDigest {
		return signalRecord{}, fmt.Errorf("%w: pending Signal content disagrees with digest", errMailboxCursor)
	}
	record.payload = payload
	return record, nil
}

func (s signalRecordWire) validateShape(sequence uint64) error {
	if !s.Source.accepts(s.ID) ||
		s.OpensWait && s.Source != signalSourceSettlement ||
		!s.OpensWait && s.Source == signalSourceSettlement && s.WaitID != nil {
		return fmt.Errorf("%w: invalid signal source", errMailboxCursor)
	}
	switch {
	case s.ArrivalSequence != sequence:
		return fmt.Errorf("%w: Signal arrival sequence disagrees with history", errMailboxCursor)
	case !s.PayloadDigest.Valid():
		return fmt.Errorf("%w: Signal payload digest is invalid", errMailboxCursor)
	case s.WaitID != nil && !s.WaitID.Valid():
		return fmt.Errorf("%w: Signal wait identity is invalid", errMailboxCursor)
	case s.OpensWait && s.WaitID == nil:
		return fmt.Errorf("%w: wait-opening Signal has no wait identity", errMailboxCursor)
	default:
		return nil
	}
}
