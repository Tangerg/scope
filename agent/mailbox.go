package agent

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
)

var (
	ErrSignalRejected = errors.New("agent: signal rejected")
	// ErrSignalConflict reports reuse of a SignalID with different immutable
	// content, including the addressed WaitID.
	ErrSignalConflict = errors.New("agent: signal identity conflicts with accepted content")
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
}

func newSignalRecord(signal Signal, opensWait bool) signalRecord {
	return signalRecord{
		id: signal.id, waitID: signal.waitID, payload: signal.payload,
		payloadDigest: ComputeDigest(signal.payload), opensWait: opensWait,
	}
}

func (s signalRecord) sameContent(other signalRecord) bool {
	return s.id == other.id && s.waitID == other.waitID && s.payloadDigest == other.payloadDigest && s.opensWait == other.opensWait
}

func (s signalRecord) snapshot() signalRecordWire {
	wire := signalRecordWire{
		ArrivalSequence: s.arrivalSequence, ID: s.id,
		PayloadDigest: s.payloadDigest, Payload: bytes.Clone(s.payload), OpensWait: s.opensWait,
	}
	if s.waitID.Valid() {
		wire.WaitID = &s.waitID
	}
	return wire
}

type waitRecord struct {
	key                   WaitKey
	id                    WaitID
	externallyAddressable bool
	answered              bool
	closed                bool
}

type signalMailbox struct {
	records      []signalRecord
	seen         map[SignalID]int
	waits        map[WaitID]waitRecord
	signalCursor uint64
}

func (s signalMailbox) clone() signalMailbox {
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

type signalSource uint8

const (
	signalSourceExternal signalSource = iota + 1
	signalSourceChildWait
)

func (s *signalMailbox) enqueue(status Status, signal Signal, source signalSource) (bool, error) {
	if !signal.Valid() || (source != signalSourceExternal && source != signalSourceChildWait) {
		return false, fmt.Errorf("%w: %w", ErrSignalRejected, ErrInvalidSignal)
	}
	return s.enqueueRecord(status, newSignalRecord(signal, false), source)
}

func (s *signalMailbox) enqueueRecord(status Status, record signalRecord, source signalSource) (bool, error) {
	if index, exists := s.seen[record.id]; exists {
		if !s.records[index].sameContent(record) {
			return false, ErrSignalConflict
		}
		return false, nil
	}
	waitID := record.waitID
	if waitID.Valid() {
		wait, exists := s.waits[waitID]
		acceptsAnswer := status == StatusRunning || status == StatusWaiting ||
			status == StatusPaused && source == signalSourceChildWait
		if !exists || wait.externallyAddressable != (source == signalSourceExternal) || wait.closed || wait.answered ||
			!acceptsAnswer {
			return false, ErrSignalRejected
		}
		wait.answered = true
		s.waits[waitID] = wait
	} else if source != signalSourceExternal || (status != StatusRunning && status != StatusPaused && status != StatusWaiting) {
		return false, ErrSignalRejected
	}
	s.appendRecord(record)
	return true, nil
}

func (s *signalMailbox) openWait(key WaitKey, signal Signal, externallyAddressable bool) error {
	_, addressed := signal.WaitID()
	if !key.Valid() || !signal.Valid() || !addressed {
		return fmt.Errorf("%w: wait key and addressed opening Signal are required", errWaitState)
	}
	return s.openWaitRecord(key, newSignalRecord(signal, true), externallyAddressable)
}

func (s *signalMailbox) openWaitRecord(key WaitKey, record signalRecord, externallyAddressable bool) error {
	id := record.waitID
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
	s.waits[id] = waitRecord{key: key, id: id, externallyAddressable: externallyAddressable}
	s.appendRecord(record)
	return nil
}

func (s *signalMailbox) appendRecord(record signalRecord) {
	s.seen[record.id] = len(s.records)
	record.arrivalSequence = uint64(len(s.records) + 1)
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
		if !record.externallyAddressable {
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
			if !s.waits[waitID].externallyAddressable {
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

func (s *signalMailbox) arrivalSequence() uint64 { return uint64(len(s.records)) }

func (s *signalMailbox) committedSignalCursor() uint64 { return s.signalCursor }

func (s *signalMailbox) pendingCount() uint64 {
	return s.arrivalSequence() - s.signalCursor
}

func (s *signalMailbox) contains(id SignalID) bool {
	_, exists := s.seen[id]
	return exists
}

type signalRecordWire struct {
	ArrivalSequence uint64          `json:"arrival_sequence"`
	ID              SignalID        `json:"id"`
	WaitID          *WaitID         `json:"wait_id,omitempty"`
	PayloadDigest   Digest          `json:"payload_digest"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	OpensWait       bool            `json:"opens_wait,omitempty"`
}

type waitRecordWire struct {
	WaitKey               WaitKey `json:"wait_key"`
	WaitID                WaitID  `json:"wait_id"`
	ExternallyAddressable bool    `json:"externally_addressable"`
	Answered              bool    `json:"answered"`
	Closed                bool    `json:"closed"`
}

type mailboxWire struct {
	Signals      []signalRecordWire `json:"signals,omitempty"`
	SignalCursor uint64             `json:"signal_cursor"`
	Waits        []waitRecordWire   `json:"waits,omitempty"`
}

func (s *signalMailbox) snapshot() mailboxWire {
	wire := mailboxWire{SignalCursor: s.signalCursor}
	for _, record := range s.records {
		wire.Signals = append(wire.Signals, record.snapshot())
	}
	for _, record := range s.waits {
		wire.Waits = append(wire.Waits, waitRecordWire{
			WaitKey: record.key, WaitID: record.id, ExternallyAddressable: record.externallyAddressable,
			Answered: record.answered, Closed: record.closed,
		})
	}
	slices.SortFunc(wire.Waits, func(left, right waitRecordWire) int {
		return cmp.Compare(left.WaitID.String(), right.WaitID.String())
	})
	return wire
}

// Restoration replays portable facts through the live mailbox transitions.
// Only final Process termination can close an unanswered or unconsumed wait.
func restoreSignalMailbox(wire mailboxWire, status Status) (signalMailbox, error) {
	if wire.SignalCursor > uint64(len(wire.Signals)) {
		return signalMailbox{}, errMailboxCursor
	}
	waits := make(map[WaitID]waitRecordWire, len(wire.Waits))
	for _, record := range wire.Waits {
		if !record.WaitKey.Valid() || !record.WaitID.Valid() {
			return signalMailbox{}, errWaitState
		}
		if _, duplicate := waits[record.WaitID]; duplicate {
			return signalMailbox{}, fmt.Errorf("%w: duplicate WaitID", errWaitState)
		}
		waits[record.WaitID] = record
	}
	mailbox := newSignalMailbox()
	for index, encoded := range wire.Signals {
		record, err := encoded.restore(uint64(index+1), wire.SignalCursor)
		if err != nil {
			return signalMailbox{}, err
		}
		if record.opensWait {
			wait, exists := waits[record.waitID]
			if !exists {
				return signalMailbox{}, fmt.Errorf("%w: opening Signal has no wait", errWaitState)
			}
			if err := mailbox.openWaitRecord(wait.WaitKey, record, wait.ExternallyAddressable); err != nil {
				return signalMailbox{}, err
			}
		} else {
			source := signalSourceExternal
			if record.waitID.Valid() && !waits[record.waitID].ExternallyAddressable {
				source = signalSourceChildWait
			}
			accepted, err := mailbox.enqueueRecord(StatusRunning, record, source)
			if err != nil || !accepted {
				return signalMailbox{}, errors.Join(err, errors.New("invalid mailbox Signal history"))
			}
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
	if len(mailbox.waits) != len(waits) {
		return signalMailbox{}, fmt.Errorf("%w: wait has no opening Signal", errWaitState)
	}
	for id, expected := range waits {
		actual := mailbox.waits[id]
		if actual.answered != expected.Answered || actual.closed != expected.Closed {
			return signalMailbox{}, fmt.Errorf("%w: wait lifecycle disagrees with Signal history", errWaitState)
		}
	}
	return mailbox, nil
}

func (s signalRecordWire) restore(sequence, cursor uint64) (signalRecord, error) {
	if s.ArrivalSequence != sequence || !s.ID.Valid() || !s.PayloadDigest.Valid() ||
		(s.WaitID != nil && !s.WaitID.Valid()) || (s.OpensWait && s.WaitID == nil) {
		return signalRecord{}, fmt.Errorf("%w: invalid Signal record", errMailboxCursor)
	}
	record := signalRecord{
		arrivalSequence: sequence, id: s.ID, payloadDigest: s.PayloadDigest, opensWait: s.OpensWait,
	}
	if s.WaitID != nil {
		record.waitID = *s.WaitID
	}
	if sequence <= cursor {
		if len(s.Payload) != 0 {
			return signalRecord{}, fmt.Errorf("%w: consumed Signal retains payload", errMailboxCursor)
		}
		return record, nil
	}
	payload, err := wireJSON.normalize(s.Payload, maxWireBytes)
	if err != nil || ComputeDigest(payload) != s.PayloadDigest {
		return signalRecord{}, fmt.Errorf("%w: pending Signal content disagrees with digest", errMailboxCursor)
	}
	record.payload = payload
	return record, nil
}
