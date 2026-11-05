package run

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

type Entry struct {
	LSN    LSN
	Epoch  Epoch
	Record Record
}

// ValidateFor admits one entry's structure and tree membership. A nonzero LSN
// is required, but the root's first Started legitimately has Epoch zero.
// Continuity, epoch progression, and replay consistency require the owning Log.
func (e Entry) ValidateFor(root ProcessID) error {
	if !root.Valid() || root.Depth() != 0 || e.LSN == 0 {
		return ErrInvalidLog
	}
	if err := validateRecord(e.Record); err != nil {
		return err
	}
	process := recordProcess(e.Record)
	if process.Valid() && process.Root() != root {
		return ErrInvalidLog
	}
	return nil
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
