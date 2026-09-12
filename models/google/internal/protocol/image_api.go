package protocol

import (
	"encoding/json"
)

type interactionErrorEnvelope struct {
	Error struct {
		Code    int               `json:"code"`
		Message string            `json:"message"`
		Status  string            `json:"status"`
		Details []json.RawMessage `json:"details,omitempty"`
	} `json:"error"`
}
