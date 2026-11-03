package eval

import (
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/Tangerg/scope/core/metadata"
)

// Policy names the rule behind a Decision. It shares Metric's namespace/name
// grammar, so every evaluation identity reads and renders one way.
type Policy struct {
	Namespace string `json:"namespace,omitzero"`
	Name      string `json:"name"`
}

func (p Policy) String() string {
	if p.Namespace == "" {
		return p.Name
	}
	return p.Namespace + "/" + p.Name
}

func (p Policy) Validate() error {
	if err := validateMetricPart("policy namespace", p.Namespace, true); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidReport, err)
	}
	if err := validateMetricPart("policy name", p.Name, false); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidReport, err)
	}
	return nil
}

// Decision records a categorical result and the policy that produced it.
// Policy and Parameters identify the rule independently of its observed Verdict.
// A Report without a categorical judgment has no Decision.
type Decision struct {
	Policy     Policy       `json:"policy"`
	Parameters metadata.Map `json:"parameters,omitzero"`
	Verdict    Verdict      `json:"verdict"`
}

func (d Decision) Validate() error {
	if err := d.Policy.Validate(); err != nil {
		return err
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
