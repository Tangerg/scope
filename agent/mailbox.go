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

// signalRecord's arrival sequence is its one-based position in the mailbox
// history, so neither the record nor its wire stores it.
type signalRecord struct {
	id            SignalID
	waitID        WaitID
	payloadDigest Digest
	payload       json.RawMessage
	opensWait     bool
	source        signalSource
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
		ID:            s.id,
		PayloadDigest: s.payloadDigest, Payload: bytes.Clone(s.payload), Source: s.source,
	}
	if s.waitID.Valid() {
		wire.WaitID = &s.waitID
	}
	return wire
}

// waitRecord owns one wait's lifecycle. A child wait also owns the spec the
// Engine evaluates against tree membership; its key is the spec's key.
type waitRecord struct {
	key      WaitKey
	child    *ChildWaitSpec
	answered bool
	closed   bool
}

func newChildWaitRecord(spec ChildWaitSpec) waitRecord {
	return waitRecord{key: spec.Key, child: new(spec.clone())}
}

func (w waitRecord) kind() WaitKind {
	if w.child != nil {
		return WaitKindChildren
	}
	return WaitKindExternal
}

// Only Host input answers an external wait; only the Engine answers a child wait.
func (w waitRecord) answerableBy(source signalSource) bool {
	return (w.child == nil) == (source == signalSourceExternal)
}

func (w waitRecord) openingWire() *waitOpeningWire {
	if w.child != nil {
		return &waitOpeningWire{Spec: new(w.child.wire())}
	}
	return &waitOpeningWire{Key: new(w.key)}
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

// openWait opens a Host-answered wait.
func (s *signalMailbox) openWait(key WaitKey, signal Signal) error {
	return s.openSettledWait(waitRecord{key: key}, signal)
}

// openChildWait opens an Engine-answered wait over spec.
func (s *signalMailbox) openChildWait(spec ChildWaitSpec, signal Signal) error {
	if !spec.Valid() {
		return fmt.Errorf("%w: %w", errWaitState, ErrInvalidChildWait)
	}
	return s.openSettledWait(newChildWaitRecord(spec), signal)
}

func (s *signalMailbox) openSettledWait(wait waitRecord, signal Signal) error {
	_, addressed := signal.WaitID()
	if !signal.Valid() || !addressed {
		return fmt.Errorf("%w: addressed opening Signal is required", errWaitState)
	}
	record := newSignalRecord(signal, true)
	record.source = signalSourceSettlement
	return s.openWaitRecord(wait, record)
}

func (s *signalMailbox) openWaitRecord(wait waitRecord, record signalRecord) error {
	id, key := record.waitID, wait.key
	if !key.Valid() {
		return fmt.Errorf("%w: invalid wait key", errWaitState)
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
	s.waits[id] = wait
	s.appendRecord(record)
	return nil
}

func (s *signalMailbox) appendRecord(record signalRecord) {
	s.seen[record.id] = len(s.records)
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

// closeAllWaits makes every remaining wait terminal with its Process.
func (s *signalMailbox) closeAllWaits() {
	for id, record := range s.waits {
		if !record.closed {
			record.closed = true
			s.waits[id] = record
		}
	}
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

func (s *signalMailbox) commit(consumedSignals uint32) error {
	remaining := uint64(len(s.records)) - s.signalCursor
	if uint64(consumedSignals) > remaining {
		return errMailboxCursor
	}
	for index := s.signalCursor; index < s.signalCursor+uint64(consumedSignals); index++ {
		record := &s.records[index]
		if waitID := record.waitID; waitID.Valid() && !record.opensWait {
			if err := s.closeWait(waitID); err != nil {
				return err
			}
		}
		// Candidate adoption owns consumption; history only needs identity,
		// content agreement, and wait facts after that boundary.
		record.payload = nil
	}
	s.signalCursor += uint64(consumedSignals)
	return nil
}

func (s *signalMailbox) acceptedCount() uint64 { return uint64(len(s.records)) }

func (s *signalMailbox) pendingCount() uint64 {
	return s.acceptedCount() - s.signalCursor
}

func (s *signalMailbox) contains(id SignalID) bool {
	_, exists := s.seen[id]
	return exists
}

type signalRecordWire struct {
	ID            SignalID         `json:"id"`
	WaitID        *WaitID          `json:"wait_id,omitzero"`
	PayloadDigest Digest           `json:"payload_digest"`
	Payload       json.RawMessage  `json:"payload,omitzero"`
	Opens         *waitOpeningWire `json:"opens,omitzero"`
	Source        signalSource     `json:"source"`
}

// waitOpeningWire records the only wait facts the Signal history cannot
// derive: an external wait's key, or a child wait's complete spec. Exactly one
// is present, and it determines the wait's kind. Whether a wait is answered or
// closed is replayed from the answers that follow its opening and from the
// Process status, never stored.
type waitOpeningWire struct {
	Key  *WaitKey           `json:"key,omitzero"`
	Spec *childWaitSpecWire `json:"spec,omitzero"`
}

func (w waitOpeningWire) clone() waitOpeningWire {
	var clone waitOpeningWire
	if w.Key != nil {
		clone.Key = new(*w.Key)
	}
	if w.Spec != nil {
		spec := *w.Spec
		spec.Children = slices.Clone(w.Spec.Children)
		clone.Spec = &spec
	}
	return clone
}

func (w waitOpeningWire) wait() (waitRecord, error) {
	switch {
	case w.Key != nil && w.Spec == nil:
		return waitRecord{key: *w.Key}, nil
	case w.Key == nil && w.Spec != nil:
		spec, err := w.Spec.value()
		if err != nil {
			return waitRecord{}, fmt.Errorf("%w: %w", errWaitState, err)
		}
		return newChildWaitRecord(spec), nil
	default:
		return waitRecord{}, fmt.Errorf("%w: wait opening needs exactly one of key and spec", errWaitState)
	}
}

func (w waitOpeningWire) kind() WaitKind {
	if w.Spec != nil {
		return WaitKindChildren
	}
	return WaitKindExternal
}

type mailboxWire struct {
	Signals      []signalRecordWire `json:"signals,omitempty"`
	SignalCursor uint64             `json:"signal_cursor"`
}

func (m mailboxWire) receipts() []SignalReceipt {
	receipts := make([]SignalReceipt, 0, len(m.Signals))
	for index, record := range m.Signals {
		arrivalSequence := uint64(index) + 1
		receipt := SignalReceipt{
			id: record.ID, waitID: lo.FromPtr(record.WaitID), payloadDigest: record.PayloadDigest,
			arrivalSequence: arrivalSequence, external: record.Source == signalSourceExternal,
		}
		if arrivalSequence > m.SignalCursor {
			receipt.pending = Signal{id: receipt.id, waitID: receipt.waitID, payload: record.Payload}
		}
		receipts = append(receipts, receipt)
	}
	return receipts
}

// openChildWaits returns the child waits that still constrain tree membership,
// in WaitID order so notification does not depend on map iteration.
func (s *signalMailbox) openChildWaits() []ChildWaitOpened {
	var waits []ChildWaitOpened
	for id, wait := range s.waits {
		if wait.child != nil && !wait.closed {
			waits = append(waits, ChildWaitOpened{waitID: id, spec: *wait.child})
		}
	}
	slices.SortFunc(waits, func(left, right ChildWaitOpened) int {
		return cmp.Compare(left.waitID.String(), right.waitID.String())
	})
	return waits
}

// awaitingChild returns the open, unanswered child waits that observe childID
// at boundary.
func (s *signalMailbox) awaitingChild(childID ProcessID, boundary ChildWaitBoundary) []ChildWaitOpened {
	var waits []ChildWaitOpened
	for _, opened := range s.openChildWaits() {
		if !s.waits[opened.waitID].answered && opened.spec.Boundary == boundary &&
			slices.Contains(opened.spec.Children, childID) {
			waits = append(waits, opened)
		}
	}
	return waits
}

func (m mailboxWire) waitKind(id WaitID) (WaitKind, bool) {
	for _, record := range m.Signals {
		if record.Opens != nil && lo.FromPtr(record.WaitID) == id {
			return record.Opens.kind(), true
		}
	}
	return WaitKindInvalid, false
}

func (s *signalMailbox) wire() mailboxWire {
	wire := mailboxWire{SignalCursor: s.signalCursor}
	for _, record := range s.records {
		encoded := record.wire()
		if record.opensWait {
			encoded.Opens = s.waits[record.waitID].openingWire()
		}
		wire.Signals = append(wire.Signals, encoded)
	}
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
	return s.waits[currentWaitID].kind() == WaitKindExternal && !currentAnswered
}

// Restoration replays portable facts through the live mailbox transitions.
// Only final Process termination can close an unanswered or unconsumed wait.
func restoreSignalMailbox(wire mailboxWire, status Status) (signalMailbox, error) {
	if wire.SignalCursor > uint64(len(wire.Signals)) {
		return signalMailbox{}, errMailboxCursor
	}
	mailbox := newSignalMailbox()
	for index, encoded := range wire.Signals {
		consumed := uint64(index) < wire.SignalCursor
		record, err := encoded.restore(consumed)
		if err != nil {
			return signalMailbox{}, err
		}
		if err := mailbox.replay(record, encoded.Opens); err != nil {
			return signalMailbox{}, err
		}
		if consumed {
			if err := mailbox.commit(1); err != nil {
				return signalMailbox{}, err
			}
		}
	}
	if status.Terminal() {
		mailbox.closeAllWaits()
	}
	return mailbox, nil
}

func (s *signalMailbox) replay(record signalRecord, opening *waitOpeningWire) error {
	if opening != nil {
		wait, err := opening.wait()
		if err != nil {
			return err
		}
		if wait.child != nil {
			// The opening Signal announced exactly this spec to the Execution.
			digest, err := childWaitOpenedDigest(*wait.child)
			if err != nil || digest != record.payloadDigest {
				return fmt.Errorf("%w: child wait disagrees with its opening Signal", errWaitState)
			}
		}
		return s.openWaitRecord(wait, record)
	}
	accepted, err := s.enqueueRecord(StatusRunning, record)
	if err != nil || !accepted {
		return errors.Join(err, errors.New("invalid mailbox Signal history"))
	}
	return nil
}

func (s signalRecordWire) restore(consumed bool) (signalRecord, error) {
	if err := s.validateShape(); err != nil {
		return signalRecord{}, err
	}
	record := signalRecord{
		id: s.ID, waitID: lo.FromPtr(s.WaitID),
		payloadDigest: s.PayloadDigest, opensWait: s.Opens != nil, source: s.Source,
	}
	if consumed {
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

func (s signalRecordWire) validateShape() error {
	if !s.Source.accepts(s.ID) ||
		s.Opens != nil && s.Source != signalSourceSettlement ||
		s.Opens == nil && s.Source == signalSourceSettlement && s.WaitID != nil {
		return fmt.Errorf("%w: invalid signal source", errMailboxCursor)
	}
	switch {
	case !s.PayloadDigest.Valid():
		return fmt.Errorf("%w: Signal payload digest is invalid", errMailboxCursor)
	case s.WaitID != nil && !s.WaitID.Valid():
		return fmt.Errorf("%w: Signal wait identity is invalid", errMailboxCursor)
	case s.Opens != nil && s.WaitID == nil:
		return fmt.Errorf("%w: wait-opening Signal has no wait identity", errMailboxCursor)
	default:
		return nil
	}
}
