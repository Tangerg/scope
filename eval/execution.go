package eval

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/metadata"
)

var ErrInvalidExecution = errors.New("eval: invalid execution")

// ExecutionStatus describes how a target stopped, independently of the quality
// of its output. A failed or exhausted execution may still produce a candidate
// that can be graded. Completed never implies a passing assessment.
type ExecutionStatus string

const (
	ExecutionCompleted       ExecutionStatus = "completed"
	ExecutionBudgetExhausted ExecutionStatus = "budget_exhausted"
	ExecutionCanceled        ExecutionStatus = "canceled"
	ExecutionFailed          ExecutionStatus = "failed"
)

// Execution is the target's receipt for one invocation. Output distinguishes a
// present empty candidate from an unavailable candidate. The target freezes its
// output before returning; referenced objects remain borrowed read-only, like
// Case.Subject. A live workspace or mutable remote handle is not a frozen output.
// The Host owns artifact storage, sandbox cleanup, and execution budgets.
type Execution[O any] struct {
	Status   ExecutionStatus `json:"status"`
	Output   *O              `json:"output,omitzero"`
	Reason   string          `json:"reason,omitzero"`
	Metadata metadata.Map    `json:"metadata,omitzero"`
}

func (e Execution[O]) Validate() error {
	switch e.Status {
	case ExecutionCompleted:
		if e.Reason != "" {
			return fmt.Errorf("%w: completed execution cannot have a stop reason", ErrInvalidExecution)
		}
	case ExecutionBudgetExhausted, ExecutionCanceled, ExecutionFailed:
		if strings.TrimSpace(e.Reason) == "" {
			return fmt.Errorf("%w: incomplete execution requires a stop reason", ErrInvalidExecution)
		}
	default:
		return fmt.Errorf("%w: unknown status %q", ErrInvalidExecution, e.Status)
	}
	if err := e.Metadata.Validate(); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrInvalidExecution, err)
	}
	return nil
}

func (e Execution[O]) snapshot() Execution[O] {
	if e.Output != nil {
		output := *e.Output
		e.Output = &output
	}
	e.Metadata = e.Metadata.Clone()
	return e
}

func (e Execution[O]) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	type wireExecution Execution[O]
	return jsonv2.Marshal(wireExecution(e))
}

func (e *Execution[O]) UnmarshalJSON(data []byte) error {
	if e == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidExecution)
	}
	var decoded struct {
		Status   ExecutionStatus `json:"status"`
		Output   json.RawMessage `json:"output,omitzero"`
		Reason   string          `json:"reason,omitzero"`
		Metadata metadata.Map    `json:"metadata,omitzero"`
	}
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidExecution, err)
	}
	candidate := Execution[O]{Status: decoded.Status, Reason: decoded.Reason, Metadata: decoded.Metadata}
	// A present JSON null can be the candidate when O is nullable. Decoding
	// directly into *O would erase its presence and turn it into no candidate.
	if decoded.Output != nil {
		candidate.Output = new(O)
		if err := jsonv2.Unmarshal(decoded.Output, candidate.Output, jsonv2.RejectUnknownMembers(true)); err != nil {
			return fmt.Errorf("%w: decode output: %w", ErrInvalidExecution, err)
		}
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	*e = candidate
	return nil
}
