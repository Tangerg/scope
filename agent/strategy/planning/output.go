package planning

import (
	"fmt"

	agent "github.com/Tangerg/scope/agent"
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

func (o Outcome) Valid() bool {
	return o == OutcomeAchieved || o == OutcomeUnreachable || o == OutcomeStuck || o == OutcomeExhausted
}

func (o Outcome) String() string {
	if !o.Valid() {
		return invalidEnumName
	}
	return string(o)
}

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

func (a AttemptStatus) Valid() bool {
	return a == AttemptSucceeded || a == AttemptFailed || a == AttemptUnconfirmed
}

func (a AttemptStatus) String() string {
	if !a.Valid() {
		return invalidEnumName
	}
	return string(a)
}

// Attempt is one final, portable Action-attempt fact. Diagnostic is empty only
// for a succeeded attempt.
type Attempt struct {
	ActionName string        `json:"action_name" jsonschema:"pattern=^[a-z][a-z0-9._-]{0\\,127}$"`
	Status     AttemptStatus `json:"status" jsonschema:"enum=succeeded,enum=failed,enum=unconfirmed"`
	Diagnostic string        `json:"diagnostic,omitempty" jsonschema:"minLength=1,maxLength=4096"`
}

func (a Attempt) excluded() bool { return a.Status != AttemptSucceeded }

func (a Attempt) Validate() error {
	if !agent.ValidQualifiedName(a.ActionName) || !a.Status.Valid() {
		return fmt.Errorf("%w: invalid Action attempt identity or status", ErrInvalidResult)
	}
	if a.Status == AttemptSucceeded {
		if a.Diagnostic != "" {
			return fmt.Errorf("%w: succeeded Action attempt has a diagnostic", ErrInvalidResult)
		}
		return nil
	}
	if !agent.ValidDiagnostic(a.Diagnostic) {
		return fmt.Errorf("%w: failed or unconfirmed Action attempt requires a bounded diagnostic", ErrInvalidResult)
	}
	return nil
}

// Output is the final semantic Planning result. WorldState is the last complete
// observation and Attempts preserve selection order. No field is derived from
// Event or Delta history.
type Output struct {
	Outcome    Outcome    `json:"outcome" jsonschema:"enum=achieved,enum=unreachable,enum=stuck,enum=exhausted"`
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
// Definition. A repeated Action name is a valid fact, even after a failed attempt.
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
