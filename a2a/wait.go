package a2a

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// WaitMetadataKey carries the WaitAddress an answering message replies to.
	WaitMetadataKey = "scope.wait"
	// AnswerSchemaMetadataKey carries the JSON Schema an answer must satisfy,
	// as exact text that survives SDK metadata conversion.
	AnswerSchemaMetadataKey = "scope.answer_schema"
	// ExternalWaitExtension is the A2A extension URI agents declare to
	// publish external waits through this metadata.
	ExternalWaitExtension = "urn:scope:a2a:external-wait"
)

// WaitAddress names one external wait: the Process that opened it and its
// key. It is structured so a child key is never mistaken for a globally unique
// wait.
type WaitAddress struct {
	Process string `json:"process"`
	Key     string `json:"key"`
}

func (w WaitAddress) Valid() bool {
	return w.Process != "" && w.Key != "" && strings.TrimSpace(w.Process) == w.Process && strings.TrimSpace(w.Key) == w.Key && utf8.ValidString(w.Process) && utf8.ValidString(w.Key)
}

func (w WaitAddress) Metadata() (map[string]any, error) {
	if !w.Valid() {
		return nil, fmt.Errorf("%w: wait requires a process and key", ErrInvalidWaitAddress)
	}
	encoded, err := jsonv2.Marshal(w)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := jsonv2.Unmarshal(encoded, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// ParseWaitAddress reads the WaitAddress an SDK metadata value carries.
func ParseWaitAddress(value any) (WaitAddress, error) {
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		return WaitAddress{}, fmt.Errorf("%w: %w", ErrInvalidWaitAddress, err)
	}
	var address WaitAddress
	if err := jsonv2.Unmarshal(encoded, &address, jsonv2.RejectUnknownMembers(true)); err != nil || !address.Valid() {
		return WaitAddress{}, fmt.Errorf("%w: wait requires a process and key", ErrInvalidWaitAddress)
	}
	return address, nil
}
