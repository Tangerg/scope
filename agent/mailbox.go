package agent

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
)

var (
	ErrSignalRejected = errors.New("agent: signal rejected")
	errMailboxCursor  = errors.New("agent: invalid signal cursor")
	errWaitState      = errors.New("agent: invalid wait state")
)

type signalRecord struct {
	arrivalSequence uint64
	signal          Signal
	opensWait       bool
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
	seen         map[SignalID]struct{}
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
		seen:  make(map[SignalID]struct{}),
		waits: make(map[WaitID]waitRecord),
	}
}

type signalSource uint8

const (
	signalSourceExternal signalSource = iota + 1
	signalSourceChildCompletion
)

func (s *signalMailbox) enqueue(status Status, signal Signal, source signalSource) (bool, error) {
	if !signal.Valid() || (source != signalSourceExternal && source != signalSourceChildCompletion) {
		return false, fmt.Errorf("%w: %w", ErrSignalRejected, ErrInvalidSignal)
	}
	if s.contains(signal.ID()) {
		return false, nil
	}
	waitID, addressed := signal.WaitID()
	if addressed {
		record, exists := s.waits[waitID]
		if !exists || record.externallyAddressable != (source == signalSourceExternal) || record.closed || record.answered ||
			(status != StatusRunning && status != StatusWaiting) {
			return false, ErrSignalRejected
		}
		record.answered = true
		s.waits[waitID] = record
	} else if source != signalSourceExternal || (status != StatusRunning && status != StatusPaused) {
		return false, ErrSignalRejected
	}
	s.seen[signal.ID()] = struct{}{}
	s.records = append(s.records, signalRecord{
		arrivalSequence: uint64(len(s.records) + 1), signal: signal,
	})
	return true, nil
}

func (s *signalMailbox) enqueueWaitOpened(signal Signal) error {
	if !signal.Valid() {
		return fmt.Errorf("%w: %w", ErrSignalRejected, ErrInvalidSignal)
	}
	waitID, addressed := signal.WaitID()
	if !addressed {
		return fmt.Errorf("%w: wait-opened Signal requires WaitID", ErrSignalRejected)
	}
	if _, exists := s.waits[waitID]; !exists {
		return fmt.Errorf("%w: wait-opened Signal addresses an unknown wait", ErrSignalRejected)
	}
	if _, duplicate := s.seen[signal.ID()]; duplicate {
		return fmt.Errorf("%w: duplicate internal SignalID", ErrSignalRejected)
	}
	s.seen[signal.ID()] = struct{}{}
	s.records = append(s.records, signalRecord{
		arrivalSequence: uint64(len(s.records) + 1),
		signal:          signal,
		opensWait:       true,
	})
	return nil
}

func (s *signalMailbox) registerWait(key WaitKey, id WaitID, externallyAddressable bool) error {
	if !key.Valid() || !id.Valid() {
		return fmt.Errorf("%w: wait key and ID are required", errWaitState)
	}
	if _, exists := s.waits[id]; exists {
		return fmt.Errorf("%w: duplicate wait ID", errWaitState)
	}
	for _, record := range s.waits {
		if record.key == key && !record.closed {
			return fmt.Errorf("%w: wait key is already open", errWaitState)
		}
	}
	s.waits[id] = waitRecord{
		key: key, id: id, externallyAddressable: externallyAddressable,
	}
	return nil
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
		signals[index] = pending[index].signal
	}
	return signals
}

func (s *signalMailbox) commit(consumedSignals uint32) ([]WaitID, error) {
	remaining := uint64(len(s.records)) - s.signalCursor
	if uint64(consumedSignals) > remaining {
		return nil, errMailboxCursor
	}
	var childWaits []WaitID
	for _, record := range s.records[s.signalCursor : s.signalCursor+uint64(consumedSignals)] {
		if waitID, addressed := record.signal.WaitID(); addressed && !record.opensWait {
			if err := s.closeWait(waitID); err != nil {
				return nil, err
			}
			if !s.waits[waitID].externallyAddressable {
				childWaits = append(childWaits, waitID)
			}
		}
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
	ArrivalSequence uint64 `json:"arrival_sequence"`
	Signal          Signal `json:"signal"`
	OpensWait       bool   `json:"opens_wait,omitempty"`
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
		wire.Signals = append(wire.Signals, signalRecordWire{
			ArrivalSequence: record.arrivalSequence, Signal: record.signal, OpensWait: record.opensWait,
		})
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

func restoreSignalMailbox(wire mailboxWire) (signalMailbox, error) {
	if wire.SignalCursor > uint64(len(wire.Signals)) {
		return signalMailbox{}, errMailboxCursor
	}
	restoration := mailboxRestoration{mailbox: newSignalMailbox()}
	restoration.mailbox.signalCursor = wire.SignalCursor
	if err := restoration.restoreSignals(wire.Signals); err != nil {
		return signalMailbox{}, err
	}
	if err := restoration.restoreWaits(wire.Waits); err != nil {
		return signalMailbox{}, err
	}
	if err := restoration.validateAddressedSignals(); err != nil {
		return signalMailbox{}, err
	}
	if err := restoration.validateAnsweredWaits(); err != nil {
		return signalMailbox{}, err
	}
	return restoration.mailbox, nil
}

type mailboxRestoration struct {
	mailbox  signalMailbox
	answered map[WaitID]struct{}
}

func (m *mailboxRestoration) restoreSignals(records []signalRecordWire) error {
	for index, record := range records {
		if record.ArrivalSequence != uint64(index+1) || !record.Signal.Valid() {
			return fmt.Errorf("%w: invalid Signal record", errMailboxCursor)
		}
		if _, duplicate := m.mailbox.seen[record.Signal.ID()]; duplicate {
			return fmt.Errorf("%w: duplicate SignalID", errMailboxCursor)
		}
		m.mailbox.seen[record.Signal.ID()] = struct{}{}
		if record.OpensWait {
			if _, addressed := record.Signal.WaitID(); !addressed {
				return fmt.Errorf("%w: wait-opened Signal has no WaitID", errWaitState)
			}
		}
		m.mailbox.records = append(m.mailbox.records, signalRecord{
			arrivalSequence: record.ArrivalSequence, signal: record.Signal, opensWait: record.OpensWait,
		})
	}
	return nil
}

func (m *mailboxRestoration) restoreWaits(records []waitRecordWire) error {
	openKeys := make(map[WaitKey]struct{})
	for _, record := range records {
		if !record.WaitKey.Valid() || !record.WaitID.Valid() {
			return errWaitState
		}
		if _, duplicate := m.mailbox.waits[record.WaitID]; duplicate {
			return fmt.Errorf("%w: duplicate WaitID", errWaitState)
		}
		if !record.Closed {
			if _, duplicate := openKeys[record.WaitKey]; duplicate {
				return fmt.Errorf("%w: duplicate open WaitKey", errWaitState)
			}
			openKeys[record.WaitKey] = struct{}{}
		}
		m.mailbox.waits[record.WaitID] = waitRecord{
			key: record.WaitKey, id: record.WaitID, externallyAddressable: record.ExternallyAddressable,
			answered: record.Answered, closed: record.Closed,
		}
	}
	return nil
}

func (m *mailboxRestoration) validateAddressedSignals() error {
	m.answered = make(map[WaitID]struct{})
	for _, record := range m.mailbox.records {
		if waitID, addressed := record.signal.WaitID(); addressed {
			wait, exists := m.mailbox.waits[waitID]
			if !exists || (!record.opensWait && !wait.answered) {
				return fmt.Errorf("%w: addressed Signal has inconsistent wait state", errWaitState)
			}
			if !record.opensWait {
				if _, duplicate := m.answered[waitID]; duplicate {
					return fmt.Errorf("%w: wait has multiple answer Signals", errWaitState)
				}
				m.answered[waitID] = struct{}{}
			}
		}
	}
	return nil
}

func (m *mailboxRestoration) validateAnsweredWaits() error {
	for id, record := range m.mailbox.waits {
		if record.answered {
			if _, exists := m.answered[id]; !exists {
				return fmt.Errorf("%w: answered wait has no Signal", errWaitState)
			}
		}
	}
	return nil
}
