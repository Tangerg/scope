package typesense

import (
	"bytes"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"

	"github.com/Tangerg/scope/core/vectorstore"
)

type importAcknowledgment uint8

const (
	importUnseen importAcknowledgment = iota
	importAccepted
	importRejected
	importContradictory
)

type importEvidence map[string]importAcknowledgment

func (i importEvidence) observe(key string, success *bool) error {
	id, err := decodeKey(key)
	previous, requested := i[id]
	if err != nil || !requested {
		return errors.New("typesense: import acknowledgment has unknown identity")
	}
	// A duplicate cannot restore evidence that an earlier response contradicted.
	if previous != importUnseen || success == nil {
		i[id] = importContradictory
		return errors.New("typesense: contradictory import acknowledgment")
	}
	if *success {
		i[id] = importAccepted
	} else {
		i[id] = importRejected
	}
	return nil
}

func (i importEvidence) outcome(ids []string) vectorstore.WriteOutcome {
	var outcome vectorstore.WriteOutcome
	for _, id := range ids {
		switch i[id] {
		case importAccepted:
			outcome.Acknowledged = append(outcome.Acknowledged, id)
		case importRejected:
			outcome.Rejected = append(outcome.Rejected, id)
		default:
			outcome.Unconfirmed = append(outcome.Unconfirmed, id)
		}
	}
	return outcome
}

func validateImport(raw []byte, ids []string) error {
	evidence := make(importEvidence, len(ids))
	for _, id := range ids {
		evidence[id] = importUnseen
	}
	decoder := jsontext.NewDecoder(bytes.NewReader(raw))
	var cause error
	for {
		var ack struct {
			ID      string `json:"id"`
			Success *bool  `json:"success"`
		}
		err := jsonv2.UnmarshalDecode(decoder, &ack)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cause = err
			break
		}
		if err := evidence.observe(ack.ID, ack.Success); err != nil {
			cause = err
		}
	}
	outcome := evidence.outcome(ids)
	if len(outcome.Acknowledged) == len(ids) && cause == nil {
		return nil
	}
	if cause == nil {
		cause = errors.New("typesense: import is incomplete or rejected")
	}
	return vectorstore.NewWriteError(cause, outcome)
}
