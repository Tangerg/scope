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
