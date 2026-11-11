package run

import (
	jsonv2 "encoding/json/v2"
	"math"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// Batch is the unit a Store appends atomically: Records placed after After by
// the writer holding Epoch. Entries derives each record's coordinates, so a
// Store never assigns positions itself.
type Batch struct {
	Root    ProcessID
	Epoch   Epoch
	After   LSN
	Records []Record
}

// A creation batch carries the limits that govern its own records.
func (b Batch) validateLimits(limits TreeLimits) error {
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
	for _, entry := range b.Entries() {
		if err := entry.ValidateFor(b.Root); err != nil {
			return err
		}
		if _, _, err := encodeRecord(entry.Record); err != nil {
			return err
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
	return b.validateActivation()
}

// validateActivation admits at most one Activated record, advancing the epoch
// by exactly one; after the first batch it must stand alone.
func (b Batch) validateActivation() error {
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
	if activations > 1 || activations != 0 && b.After != 0 && len(b.Records) != 1 {
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
	return b.admitValidated(current, head, existing)
}

// MemoryStore already validated and detached its batch before taking the append
// lock, so rechecking its structure would re-encode unchanged payloads.
func (b Batch) admitValidated(current Epoch, head LSN, existing []Entry) (next Epoch, retry bool, err error) {
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

func (b Batch) Clone() (Batch, error) {
	if err := b.Validate(); err != nil {
		return Batch{}, err
	}
	owned := b
	owned.Records = make([]Record, len(b.Records))
	var err error
	for index, record := range b.Records {
		owned.Records[index], err = cloneRecord(record)
		if err != nil {
			return Batch{}, err
		}
	}
	return owned, nil
}
