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
		acceptsAnswer := status == StatusRunning || status == StatusWaiting ||
			status == StatusPaused && source == signalSourceChildCompletion
		if !exists || record.externallyAddressable != (source == signalSourceExternal) || record.closed || record.answered ||
			!acceptsAnswer {
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

func (s *signalMailbox) openWait(key WaitKey, signal Signal, externallyAddressable bool) error {
	id, addressed := signal.WaitID()
	if !key.Valid() || !signal.Valid() || !addressed {
		return fmt.Errorf("%w: wait key and addressed opening Signal are required", errWaitState)
	}
	if _, exists := s.waits[id]; exists {
		return fmt.Errorf("%w: duplicate wait ID", errWaitState)
	}
	if s.contains(signal.ID()) {
		return fmt.Errorf("%w: duplicate opening SignalID", errWaitState)
	}
	for _, record := range s.waits {
		if record.key == key && !record.closed {
			return fmt.Errorf("%w: wait key is already open", errWaitState)
		}
	}
	s.waits[id] = waitRecord{key: key, id: id, externallyAddressable: externallyAddressable}
	s.seen[signal.ID()] = struct{}{}
	s.records = append(s.records, signalRecord{
		arrivalSequence: uint64(len(s.records) + 1), signal: signal, opensWait: true,
	})
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
	for index, record := range wire.Signals {
		if record.ArrivalSequence != uint64(index+1) || !record.Signal.Valid() {
			return signalMailbox{}, fmt.Errorf("%w: invalid Signal record", errMailboxCursor)
		}
		if record.OpensWait {
			id, _ := record.Signal.WaitID()
			wait, exists := waits[id]
			if !exists {
				return signalMailbox{}, fmt.Errorf("%w: opening Signal has no wait", errWaitState)
			}
			if err := mailbox.openWait(wait.WaitKey, record.Signal, wait.ExternallyAddressable); err != nil {
				return signalMailbox{}, err
			}
		} else {
			source := signalSourceExternal
			if id, addressed := record.Signal.WaitID(); addressed && !waits[id].ExternallyAddressable {
				source = signalSourceChildCompletion
			}
			accepted, err := mailbox.enqueue(StatusRunning, record.Signal, source)
			if err != nil || !accepted {
				return signalMailbox{}, errors.Join(err, errors.New("invalid mailbox Signal history"))
			}
		}
		if record.ArrivalSequence <= wire.SignalCursor {
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
