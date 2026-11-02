package a2a

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"strings"
	"unicode/utf8"
)

// The address is structured so a child key cannot be mistaken for a globally
// unique wait. Schema JSON remains exact text across SDK metadata conversion.
const WaitMetadataKey = "scope.wait"
const AnswerSchemaMetadataKey = "scope.answer_schema"
const ExternalWaitExtension = "urn:scope:a2a:external-wait"

type WaitAddress struct {
	Process string `json:"process"`
	Key     string `json:"key"`
}

func (w WaitAddress) Valid() bool {
	return w.Process != "" && w.Key != "" && strings.TrimSpace(w.Process) == w.Process && strings.TrimSpace(w.Key) == w.Key && utf8.ValidString(w.Process) && utf8.ValidString(w.Key)
}

func (w WaitAddress) Metadata() (map[string]any, error) {
	if !w.Valid() {
		return nil, fmt.Errorf("%w: wait requires a process and key", ErrInvalidResult)
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

func ParseWaitAddress(value any) (WaitAddress, error) {
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		return WaitAddress{}, fmt.Errorf("%w: wait address: %w", ErrInvalidResult, err)
	}
	var address WaitAddress
	if err := jsonv2.Unmarshal(encoded, &address, jsonv2.RejectUnknownMembers(true)); err != nil || !address.Valid() {
		return WaitAddress{}, fmt.Errorf("%w: wait requires a process and key", ErrInvalidResult)
	}
	return address, nil
}
