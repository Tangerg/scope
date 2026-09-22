package etl

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"

	"github.com/Tangerg/scope/core/metadata"
)

// metadataValue owns the deterministic text representation used by document
// formatters.
type metadataValue json.RawMessage

func (m metadataValue) text() (string, error) {
	value := bytes.TrimSpace(m)
	if !jsontext.Value(value).IsValid() {
		return "", metadata.ErrInvalidValue
	}
	if bytes.Equal(value, []byte("null")) {
		return "", nil
	}
	if value[0] == '"' {
		var text string
		if err := jsonv2.Unmarshal(value, &text); err != nil {
			return "", err
		}
		return text, nil
	}
	compact := jsontext.Value(value).Clone()
	if err := compact.Compact(); err != nil {
		return "", err
	}
	return compact.String(), nil
}
