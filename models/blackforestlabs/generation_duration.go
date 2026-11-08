package blackforestlabs

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
)

// The native numeric token preserves precision without inventing an undocumented
// unit. json.Number alone also accepts quoted numbers, which this wire field does not.
type generationDuration struct {
	number json.Number
}

func (g *generationDuration) UnmarshalJSON(data []byte) error {
	raw := jsontext.Value(bytes.TrimSpace(data))
	if raw.Kind() != jsontext.KindNumber || !raw.IsValid() {
		return errors.New("blackforestlabs: result duration must be a number")
	}
	number := json.Number(raw)
	value, err := number.Float64()
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) || value < 0 {
		return errors.New("blackforestlabs: result duration must be a finite nonnegative number")
	}
	g.number = number
	return nil
}

func (g generationDuration) MarshalJSON() ([]byte, error) {
	if g.number == "" {
		return nil, errors.New("blackforestlabs: result duration is absent")
	}
	return jsonv2.Marshal(g.number)
}
