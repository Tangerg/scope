package planning

import (
	"fmt"
	"slices"

	"github.com/Tangerg/scope/agent/internal/enum"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// Outcome is the Planning-owned semantic reason a Goal-directed execution
// completed. It does not add states to the common Process lifecycle.
type Outcome string

const (
	OutcomeInvalid Outcome = ""
	// OutcomeAchieved means the latest observed WorldState satisfies the Goal.
	OutcomeAchieved Outcome = "achieved"
	// OutcomeUnreachable means the initial complete planning search found no plan.
	OutcomeUnreachable Outcome = "unreachable"
	// OutcomeStuck means that after attempted Actions a further planning pass
	// found no plan to the Goal.
	OutcomeStuck Outcome = "stuck"
	// OutcomeExhausted means the Action attempt limit admitted no further
	// attempt, so no further planning pass ran.
	OutcomeExhausted Outcome = "exhausted"
)

var outcomeVocabulary = []Outcome{OutcomeAchieved, OutcomeUnreachable, OutcomeStuck, OutcomeExhausted}

func (o Outcome) Valid() bool { return slices.Contains(outcomeVocabulary, o) }

func (o Outcome) String() string { return enum.String(o) }

func (Outcome) JSONSchemaEnum() []string { return enumNames(outcomeVocabulary) }

// AttemptStatus records the observed result of one selected Action attempt.
type AttemptStatus string

const (
	AttemptInvalid AttemptStatus = ""
	// AttemptSucceeded means execution succeeded and reobservation established
	// every predicted effect.
	AttemptSucceeded AttemptStatus = "succeeded"
	// AttemptFailed means the dispatcher or child Process definitely failed.
	AttemptFailed AttemptStatus = "failed"
	// AttemptUnconfirmed means execution reported success but reobservation did
	// not establish every predicted effect.
	AttemptUnconfirmed AttemptStatus = "unconfirmed"
)

var attemptStatusVocabulary = []AttemptStatus{AttemptSucceeded, AttemptFailed, AttemptUnconfirmed}

func (a AttemptStatus) Valid() bool { return slices.Contains(attemptStatusVocabulary, a) }

func (a AttemptStatus) String() string { return enum.String(a) }

func (AttemptStatus) JSONSchemaEnum() []string { return enumNames(attemptStatusVocabulary) }

func enumNames[T ~string](values []T) []string {
	names := make([]string, len(values))
	for index, value := range values {
		names[index] = string(value)
	}
	return names
}

// Attempt is one final, portable Action-attempt fact. Only a failed attempt
// has a Diagnostic: an unconfirmed attempt is fully explained by its Status.
type Attempt struct {
	ActionName run.QualifiedName `json:"action_name"`
	Status     AttemptStatus     `json:"status"`
	Diagnostic string            `json:"diagnostic,omitempty"`
}

func (a Attempt) excluded() bool { return a.Status != AttemptSucceeded }

func (a Attempt) Validate() error {
	if !run.ValidQualifiedName(string(a.ActionName)) || !a.Status.Valid() {
		return fmt.Errorf("%w: invalid Action attempt identity or status", ErrInvalidResult)
	}
	if a.Status != AttemptFailed {
		if a.Diagnostic != "" {
			return fmt.Errorf("%w: only a failed Action attempt has a diagnostic", ErrInvalidResult)
		}
		return nil
	}
	if !run.ValidDiagnostic(a.Diagnostic) {
		return fmt.Errorf("%w: failed Action attempt requires a bounded diagnostic", ErrInvalidResult)
	}
	return nil
}

// Output is the final semantic Planning result. WorldState is the last complete
// observation and Attempts preserve selection order. No field is derived from
// Event or Delta history.
type Output struct {
	Outcome    Outcome    `json:"outcome"`
	WorldState WorldState `json:"world_state"`
	Attempts   []Attempt  `json:"attempts"`
}

// PlanningPasses counts calls to Planner: every attempt followed one pass, and
// an unreachable or stuck result followed one more pass that found no plan.
func (o Output) PlanningPasses() uint64 {
	passes := uint64(len(o.Attempts))
	if o.Outcome == OutcomeUnreachable || o.Outcome == OutcomeStuck {
		passes++
	}
	return passes
}

// Validate checks the outcome against the ordered attempt facts. Goal
// satisfaction, Action membership, and admission policy require the owning
// definition. A repeated Action name is a valid fact, even after a failed attempt.
func (o Output) Validate() error {
	if !o.Outcome.Valid() {
		return fmt.Errorf("%w: invalid output outcome", ErrInvalidResult)
	}
	if err := validateAttempts(o.Attempts); err != nil {
		return err
	}
	switch o.Outcome {
	case OutcomeUnreachable:
		if len(o.Attempts) != 0 {
			return fmt.Errorf("%w: unreachable output has attempted Actions", ErrInvalidResult)
		}
	case OutcomeStuck, OutcomeExhausted:
		if len(o.Attempts) == 0 {
			return fmt.Errorf("%w: %s output requires attempted Actions", ErrInvalidResult, o.Outcome)
		}
	}
	return nil
}

func validateAttempts(attempts []Attempt) error {
	for index, attempt := range attempts {
		if err := attempt.Validate(); err != nil {
			return fmt.Errorf("attempt %d: %w", index, err)
		}
	}
	return nil
}

// JSON Schema counts characters; Validate enforces the complete UTF-8 byte bound.
func (Attempt) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{"diagnostic": {MinLength: new(uint64(1)), MaxLength: new(uint64(run.MaxDiagnosticBytes))}}}
}
