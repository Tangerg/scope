package agent

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// Quota is an immutable optional upper bound. Its zero value is unlimited;
// NewQuota constructs a finite bound, including zero. A Quota is policy, never
// a usage counter or a reservation. Its JSON object preserves this distinction:
// {"maximum":null} is unlimited and {"maximum":0} permits no work.
type Quota struct {
	maximum uint64
	limited bool
}

// NewQuota constructs a finite inclusive upper bound.
func NewQuota(maximum uint64) Quota { return Quota{maximum: maximum, limited: true} }

// Maximum returns the finite upper bound, or false for an unlimited quota.
func (q Quota) Maximum() (uint64, bool) { return q.maximum, q.limited }

// Allows reports whether all quantities fit together, without unsigned overflow.
// Unlimited quotas impose no bound; callers still own counter and identity overflow.
func (q Quota) Allows(quantities ...uint64) bool {
	return !q.limited || resourceQuantitiesFit(q.maximum, quantities...)
}

func (q Quota) allocation(child Quota) (uint64, bool) {
	if !q.limited {
		return 0, true
	}
	return child.maximum, child.limited && child.maximum <= q.maximum
}

func (q Quota) MarshalJSON() ([]byte, error) {
	wire := quotaWire{}
	if q.limited {
		wire.Maximum = new(q.maximum)
	}
	return json.Marshal(wire)
}

func (q *Quota) UnmarshalJSON(data []byte) error {
	if q == nil {
		return errors.New("agent: nil quota receiver")
	}
	wire, err := jsonwire.Decode[struct {
		Maximum json.RawMessage `json:"maximum"`
	}](data)
	if err != nil {
		return err
	}
	if len(wire.Maximum) == 0 {
		return errors.New("agent: quota maximum is required")
	}
	if bytes.Equal(bytes.TrimSpace(wire.Maximum), []byte("null")) {
		*q = Quota{}
		return nil
	}
	maximum, err := jsonwire.Decode[uint64](wire.Maximum)
	if err != nil {
		return err
	}
	*q = NewQuota(maximum)
	return nil
}

func (Quota) JSONSchemaAlias() any { return quotaWire{} }

type quotaWire struct {
	Maximum *uint64 `json:"maximum" jsonschema:"nullable"`
}
