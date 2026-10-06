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
// history and its source follows from its identity facts, so neither the
// record nor its wire stores them.
type signalRecord struct {
	id            SignalID
	waitID        WaitID
	payloadDigest Digest
	payload       json.RawMessage
	opensWait     bool
}

func newSignalRecord(signal Signal, opensWait bool) signalRecord {
	return signalRecord{
		id: signal.id, waitID: signal.waitID, payload: signal.payload,
		payloadDigest: ComputeDigest(signal.payload), opensWait: opensWait,
	}
}

// newAdmissionRecord accepts signal only through the channel its identity
// facts assign it to.
func newAdmissionRecord(signal Signal, source signalSource) (signalRecord, error) {
	record := newSignalRecord(signal, false)
	if !signal.Valid() || record.source() != source {
		return signalRecord{}, fmt.Errorf("%w: %w", ErrSignalRejected, ErrInvalidSignal)
	}
	return record, nil
}

// source classifies who may deliver the record: Host identities are external;
// an Engine identity opens a wait or settles an Effect as a settlement, and
// answers a child wait when addressed.
func (s signalRecord) source() signalSource {
	switch {
	case !s.id.engineOwned():
		return signalSourceExternal
	case s.waitID.Valid() && !s.opensWait:
		return signalSourceChildWait
	default:
		return signalSourceSettlement
	}
}

func (s signalRecord) sameContent(other signalRecord) bool {
	return s.id == other.id && s.waitID == other.waitID && s.payloadDigest == other.payloadDigest && s.opensWait == other.opensWait
}

// wire keeps a pending payload or a consumed digest, never both.
func (s signalRecord) wire() signalRecordWire {
	wire := signalRecordWire{ID: s.id, Payload: bytes.Clone(s.payload)}
	if s.payload == nil {
		wire.PayloadDigest = new(s.payloadDigest)
	}
	if s.waitID.Valid() {
		wire.WaitID = &s.waitID
	}
	return wire
}

// waitRecord owns one wait's lifecycle. A child wait also owns the spec the
// Engine evaluates against tree membership; its key is the spec's key.
type waitRecord struct {
	externalKey WaitKey
	child       *ChildWaitSpec
	answered    bool
	closed      bool
}

func newChildWaitRecord(spec ChildWaitSpec) waitRecord {
	return waitRecord{child: new(spec.clone())}
}

func (w waitRecord) key() WaitKey {
	if w.child != nil {
		return w.child.Key
	}
	return w.externalKey
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
	return &waitOpeningWire{Key: new(w.externalKey)}
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
		return true, nil
	}
	wait, exists := s.waits[record.waitID]
	if !exists || !wait.answerableBy(record.source()) || wait.closed || wait.answered {
		return false, ErrSignalRejected
	}
	return true, nil
}

func (s *signalMailbox) validateDuplicate(existing, record signalRecord) error {
	if !existing.sameContent(record) {
		return ErrSignalConflict
	}
	if record.waitID.Valid() && !s.waits[record.waitID].answerableBy(record.source()) {
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

// openWait opens a Host-answered wait; its opening Signal only acknowledges
// the minted WaitID.
func (s *signalMailbox) openWait(key WaitKey, signal Signal) error {
	if !bytes.Equal(waitOpenedPayload(), signal.payload) {
		return fmt.Errorf("%w: wait opening Signal must be its acknowledgement", errWaitState)
	}
	return s.openSettledWait(waitRecord{externalKey: key}, signal)
}

// openChildWait opens an Engine-answered wait over spec, which the persisted
// opening keeps; its opening Signal only acknowledges the minted WaitID.
func (s *signalMailbox) openChildWait(spec ChildWaitSpec, signal Signal) error {
	if !spec.Valid() {
		return fmt.Errorf("%w: %w", errWaitState, ErrInvalidChildWait)
	}
	if !bytes.Equal(childWaitOpenedPayload(), signal.payload) {
		return fmt.Errorf("%w: child wait opening Signal must be its acknowledgement", errWaitState)
	}
	return s.openSettledWait(newChildWaitRecord(spec), signal)
}

func (s *signalMailbox) openSettledWait(wait waitRecord, signal Signal) error {
	_, addressed := signal.WaitID()
	if !signal.Valid() || !addressed {
		return fmt.Errorf("%w: addressed opening Signal is required", errWaitState)
	}
	return s.openWaitRecord(wait, newSignalRecord(signal, true))
}

func (s *signalMailbox) openWaitRecord(wait waitRecord, record signalRecord) error {
	id, key := record.waitID, wait.key()
	if !key.Valid() {
		return fmt.Errorf("%w: invalid wait key", errWaitState)
	}
	if record.source() != signalSourceSettlement {
		return fmt.Errorf("%w: opening Signal requires Engine identity", errWaitState)
	}
	if _, exists := s.waits[id]; exists {
		return fmt.Errorf("%w: duplicate wait ID", errWaitState)
	}
	if s.contains(record.id) {
		return fmt.Errorf("%w: duplicate opening SignalID", errWaitState)
	}
	for _, wait := range s.waits {
		if wait.key() == key && !wait.closed {
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

// awaited returns entered while this mailbox has not answered it: the wait a
// Step entered keeps its Process Waiting only until its answer arrives.
func (s *signalMailbox) awaited(entered WaitID) WaitID {
	if !entered.Valid() || s.waits[entered].answered {
		return WaitID{}
	}
	return entered
}

// enterWait admits id as the wait a Step enters; an early answer leaves the
// Process runnable without another transition.
func (s *signalMailbox) enterWait(id WaitID) error {
	record, exists := s.waits[id]
	if !exists || record.closed {
		return errWaitState
	}
	return nil
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

// signalRecordWire keeps a pending record's payload and a consumed record's
// digest. A wait opening keeps neither, because every opening of its kind
// carries the same acknowledgement.
type signalRecordWire struct {
	ID            SignalID         `json:"id"`
	WaitID        *WaitID          `json:"wait_id,omitzero"`
	PayloadDigest *Digest          `json:"payload_digest,omitzero"`
	Payload       json.RawMessage  `json:"payload,omitzero"`
	Opens         *waitOpeningWire `json:"opens,omitzero"`
}

// content derives the payload a pending record still carries and the digest
// every record identifies.
func (s signalRecordWire) content(consumed bool) (json.RawMessage, Digest, error) {
	if s.Opens != nil {
		if s.Payload != nil || s.PayloadDigest != nil {
			return nil, Digest{}, fmt.Errorf("%w: wait opening stores its fixed acknowledgement", errMailboxCursor)
		}
		payload := waitOpenedPayload()
		if s.Opens.Spec != nil {
			payload = childWaitOpenedPayload()
		}
		if consumed {
			return nil, ComputeDigest(payload), nil
		}
		return payload, ComputeDigest(payload), nil
	}
	if consumed {
		if s.Payload != nil || s.PayloadDigest == nil || !s.PayloadDigest.Valid() {
			return nil, Digest{}, fmt.Errorf("%w: consumed Signal keeps exactly its digest", errMailboxCursor)
		}
		return nil, *s.PayloadDigest, nil
	}
	if s.PayloadDigest != nil {
		return nil, Digest{}, fmt.Errorf("%w: pending Signal stores a digest of its payload", errMailboxCursor)
	}
	payload, err := normalizeJSON(s.Payload, MaxPayloadBytes)
	if err != nil {
		return nil, Digest{}, fmt.Errorf("%w: pending Signal payload: %w", errMailboxCursor, err)
	}
	return payload, ComputeDigest(payload), nil
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
		return waitRecord{externalKey: *w.Key}, nil
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

// mailboxDocument is the persisted mailbox. A wait opening and a child-wait
// answer omit the SignalID their wait derives; a pending answer keeps only
// the children it
// answered with, in request order: their retained records determine its
// outcomes, so only the enclosing tree can decode it.
type mailboxDocument struct {
	Signals      []signalRecordDocument `json:"signals,omitempty"`
	SignalCursor uint64                 `json:"signal_cursor"`
}

type signalRecordDocument struct {
	signalRecordWire
	ID       *SignalID   `json:"id,omitzero"`
	Answered []ProcessID `json:"answered,omitempty"`
}

// record restores the record's identity: a wait opening and a child-wait
// answer are the records addressed to a wait without naming their own
// SignalID, which that wait derives.
func (s signalRecordDocument) record() (signalRecordWire, error) {
	record := s.signalRecordWire
	if s.ID != nil {
		record.ID = *s.ID
		if record.Opens != nil || record.answersChildWait() {
			return signalRecordWire{}, fmt.Errorf("%w: record stores the SignalID its wait derives", errMailboxCursor)
		}
		return record, nil
	}
	switch {
	case record.WaitID == nil:
		return signalRecordWire{}, fmt.Errorf("%w: only a wait's own records omit their SignalID", errMailboxCursor)
	case record.Opens != nil:
		record.ID = record.WaitID.openingSignalID()
	default:
		record.ID = record.WaitID.childWaitSignalID()
	}
	return record, nil
}

// childOutcomeSource derives the outcome a direct child reached at boundary.
type childOutcomeSource func(child ProcessID, boundary ChildWaitBoundary) (ChildOutcome, error)

func (m mailboxWire) document() (mailboxDocument, error) {
	document := mailboxDocument{SignalCursor: m.SignalCursor, Signals: make([]signalRecordDocument, len(m.Signals))}
	for index, record := range m.Signals {
		document.Signals[index].signalRecordWire = record
		if record.Opens != nil {
			continue
		}
		if !record.answersChildWait() {
			document.Signals[index].ID = new(record.ID)
			continue
		}
		if uint64(index) < m.SignalCursor {
			continue
		}
		signal, err := NewSignal(record.ID, *record.WaitID, record.Payload)
		if err != nil {
			return mailboxDocument{}, err
		}
		satisfied, err := ParseChildWaitSatisfied(signal)
		if err != nil {
			return mailboxDocument{}, err
		}
		answered := make([]ProcessID, len(satisfied.outcomes))
		for position, outcome := range satisfied.outcomes {
			answered[position] = outcome.result.ProcessID()
		}
		document.Signals[index].Payload = nil
		document.Signals[index].Answered = answered
	}
	return document, nil
}

// wire renders each pending child-wait answer from the outcomes its answered
// children reached; the wait it answers supplies the boundary.
func (m mailboxDocument) wire(outcomes childOutcomeSource) (mailboxWire, error) {
	wire := mailboxWire{SignalCursor: m.SignalCursor, Signals: make([]signalRecordWire, len(m.Signals))}
	specs := make(map[WaitID]childWaitSpecWire)
	for index, document := range m.Signals {
		record, err := document.record()
		if err != nil {
			return mailboxWire{}, err
		}
		if record.Opens != nil && record.Opens.Spec != nil && record.WaitID != nil {
			specs[*record.WaitID] = *record.Opens.Spec
		}
		pendingAnswer := uint64(index) >= m.SignalCursor && record.answersChildWait()
		if pendingAnswer != (document.Answered != nil) {
			return mailboxWire{}, fmt.Errorf("%w: exactly a pending child-wait answer names its answered children", errMailboxCursor)
		}
		if pendingAnswer {
			payload, err := renderChildWaitAnswer(record, specs[*record.WaitID], document.Answered, outcomes)
			if err != nil {
				return mailboxWire{}, err
			}
			record.Payload = payload
		}
		wire.Signals[index] = record
	}
	return wire, nil
}

func renderChildWaitAnswer(record signalRecordWire, opening childWaitSpecWire, answered []ProcessID, outcomes childOutcomeSource) (json.RawMessage, error) {
	if record.Payload != nil || record.PayloadDigest != nil {
		return nil, fmt.Errorf("%w: pending child-wait answer stores content its children determine", errMailboxCursor)
	}
	waitID := *record.WaitID
	spec, err := opening.value()
	if err != nil {
		return nil, fmt.Errorf("%w: child-wait answer needs its opened wait: %w", errWaitState, err)
	}
	satisfied := ChildWaitSatisfied{waitID: waitID}
	for _, child := range answered {
		outcome, outcomeErr := outcomes(child, spec.Boundary)
		if outcomeErr != nil {
			return nil, fmt.Errorf("%w: child-wait answer: %w", errWaitState, outcomeErr)
		}
		satisfied.outcomes = append(satisfied.outcomes, outcome)
	}
	if !satisfied.Matches(waitID, spec) {
		return nil, fmt.Errorf("%w: answered children do not satisfy their wait", errWaitState)
	}
	signal, err := encodeChildWaitSatisfied(waitID, satisfied.outcomes)
	if err != nil {
		return nil, fmt.Errorf("%w: child-wait answer: %w", errWaitState, err)
	}
	return signal.payload, nil
}

func (m mailboxWire) receipts() []SignalReceipt {
	receipts := make([]SignalReceipt, 0, len(m.Signals))
	for index, record := range m.Signals {
		arrivalSequence := uint64(index) + 1
		consumed := arrivalSequence <= m.SignalCursor
		// A validated capture always derives its content.
		payload, digest, _ := record.content(consumed)
		receipt := SignalReceipt{
			id: record.ID, waitID: lo.FromPtr(record.WaitID), payloadDigest: digest,
			arrivalSequence: arrivalSequence, external: !record.ID.engineOwned(),
		}
		if !consumed {
			receipt.pending = Signal{id: receipt.id, waitID: receipt.waitID, payload: payload}
		}
		receipts = append(receipts, receipt)
	}
	return receipts
}

// openedChildWait is a child wait the mailbox opened, with the spec it owns.
type openedChildWait struct {
	waitID WaitID
	spec   ChildWaitSpec
}

// openChildWaits returns the child waits that still constrain tree membership,
// in WaitID order so notification does not depend on map iteration.
func (s *signalMailbox) openChildWaits() []openedChildWait {
	var waits []openedChildWait
	for id, wait := range s.waits {
		if wait.child != nil && !wait.closed {
			waits = append(waits, openedChildWait{waitID: id, spec: *wait.child})
		}
	}
	slices.SortFunc(waits, func(left, right openedChildWait) int {
		return cmp.Compare(left.waitID.String(), right.waitID.String())
	})
	return waits
}

// awaitingChild returns the open, unanswered child waits that observe childID
// at boundary.
func (s *signalMailbox) awaitingChild(childID ProcessID, boundary ChildWaitBoundary) []openedChildWait {
	var waits []openedChildWait
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
			wait := s.waits[record.waitID]
			encoded.Opens = wait.openingWire()
			encoded.Payload, encoded.PayloadDigest = nil, nil
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
// A wait closes only when a Step consumes its answer.
func restoreSignalMailbox(wire mailboxWire) (signalMailbox, error) {
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
	return mailbox, nil
}

func (s *signalMailbox) replay(record signalRecord, opening *waitOpeningWire) error {
	if opening != nil {
		wait, err := opening.wait()
		if err != nil {
			return err
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
	payload, digest, err := s.content(consumed)
	if err != nil {
		return signalRecord{}, err
	}
	return signalRecord{
		id: s.ID, waitID: lo.FromPtr(s.WaitID),
		payloadDigest: digest, payload: payload, opensWait: s.Opens != nil,
	}, nil
}

// answersChildWait follows the record's identity facts: only the Engine
// addresses a Signal to a wait it did not open by that Signal.
func (s signalRecordWire) answersChildWait() bool {
	return s.ID.engineOwned() && s.WaitID != nil && s.Opens == nil
}

func (s signalRecordWire) validateShape() error {
	if !s.ID.Valid() || s.Opens != nil && !s.ID.engineOwned() {
		return fmt.Errorf("%w: invalid signal source", errMailboxCursor)
	}
	switch {
	case s.WaitID != nil && !s.WaitID.Valid():
		return fmt.Errorf("%w: Signal wait identity is invalid", errMailboxCursor)
	case s.Opens != nil && s.WaitID == nil:
		return fmt.Errorf("%w: wait-opening Signal has no wait identity", errMailboxCursor)
	default:
		return nil
	}
}
