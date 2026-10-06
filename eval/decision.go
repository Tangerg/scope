package eval

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/metadata"
)

// Decision records a categorical result and the policy that produced it.
// Policy and Parameters identify the rule independently of its observed Verdict.
// A Report without a categorical judgment has no Decision.
type Decision struct {
	Policy     string       `json:"policy"`
	Parameters metadata.Map `json:"parameters,omitzero"`
	Verdict    Verdict      `json:"verdict"`
}

func (d Decision) Validate() error {
	if !utf8.ValidString(d.Policy) {
		return fmt.Errorf("%w: decision policy must be valid UTF-8", ErrInvalidReport)
	}
	if d.Policy == "" || strings.TrimSpace(d.Policy) != d.Policy {
		return fmt.Errorf("%w: decision policy must be non-empty without surrounding whitespace", ErrInvalidReport)
	}
	if !d.Verdict.Decided() {
		return fmt.Errorf("%w: decision verdict must be pass or fail", ErrInvalidReport)
	}
	if err := d.Parameters.Validate(); err != nil {
		return fmt.Errorf("%w: decision parameters: %w", ErrInvalidReport, err)
	}
	return nil
}

func (d Decision) clone() Decision {
	d.Parameters = d.Parameters.Clone()
	return d
}

func (d Decision) identity() (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	encoded, err := jsonv2.Marshal(struct {
		Policy     string       `json:"policy"`
		Parameters metadata.Map `json:"parameters,omitzero"`
	}{Policy: d.Policy, Parameters: d.Parameters})
	if err != nil {
		return "", err
	}
	value := jsontext.Value(encoded)
	if err := value.Format(jsontext.ReorderRawObjects(true)); err != nil {
		return "", err
	}
	return string(value), nil
}

func (d Decision) MarshalJSON() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	type wireDecision Decision
	return jsonv2.Marshal(wireDecision(d))
}

func (d *Decision) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("%w: nil decision receiver", ErrInvalidReport)
	}
	type wireDecision Decision
	var decoded wireDecision
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode decision: %w", ErrInvalidReport, err)
	}
	candidate := Decision(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*d = candidate
	return nil
}
