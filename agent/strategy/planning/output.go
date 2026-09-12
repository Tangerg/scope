package planning

import (
	"errors"
	"fmt"
)

// Outcome is the Planning-owned semantic reason a Goal-directed execution
// completed. It does not add states to the common Process lifecycle.
type Outcome string

const (
	// OutcomeAchieved means the latest observed WorldState satisfies the Goal.
	OutcomeAchieved Outcome = "achieved"
	// OutcomeUnreachable means the initial complete planning search found no plan.
	OutcomeUnreachable Outcome = "unreachable"
	// OutcomeStuck means attempts or the Action limit were exhausted without
	// reaching the Goal.
	OutcomeStuck Outcome = "stuck"
)

func (o Outcome) Valid() bool {
	return o == OutcomeAchieved || o == OutcomeUnreachable || o == OutcomeStuck
}

// AttemptStatus records the observed result of one selected Action attempt.
type AttemptStatus string

const (
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

// Attempt is one final, portable Action-attempt fact. Diagnostic is empty only
// for a succeeded attempt.
type Attempt struct {
	// ActionName is the exact Action identity selected for this attempt.
	ActionName string `json:"action_name" jsonschema:"pattern=^[a-z][a-z0-9._-]{0\\,127}$"`
	// Status is the observed semantic outcome of this attempt.
	Status AttemptStatus `json:"status" jsonschema:"enum=succeeded,enum=failed,enum=unconfirmed"`
	// Diagnostic explains failed or unconfirmed attempts and is empty on success.
	Diagnostic string `json:"diagnostic,omitempty" jsonschema:"minLength=1,maxLength=4096"`
}

func (a Attempt) Validate() error {
	if !validName(a.ActionName) || !a.Status.Valid() {
		return errors.New("planning: invalid Action attempt identity or status")
	}
	if a.Status == AttemptSucceeded {
		if a.Diagnostic != "" {
			return errors.New("planning: succeeded Action attempt has a diagnostic")
		}
		return nil
	}
	if !validDiagnostic(a.Diagnostic) {
		return errors.New("planning: failed or unconfirmed Action attempt requires a bounded diagnostic")
	}
	return nil
}

// Output is the final semantic Planning result. WorldState is the last complete
// observation, Attempts preserve selection order, and PlanningPasses counts
// calls to Planner. No field is derived from Event or Delta history.
type Output struct {
	// Outcome is the Planning-owned semantic completion reason.
	Outcome Outcome `json:"outcome" jsonschema:"enum=achieved,enum=unreachable,enum=stuck"`
	// WorldState is the final complete observation.
	WorldState WorldState `json:"world_state"`
	// Attempts preserves Action-attempt order.
	Attempts []Attempt `json:"attempts"`
	// PlanningPasses counts calls to Planner.
	PlanningPasses uint32 `json:"planning_passes" jsonschema:"maximum=4294967295"`
}

// Validate checks completed planning counters and ordered attempt facts. Goal
// satisfaction, Action membership, and admission policy require the owning
// Definition. A repeated Action name is a valid fact, even after a failed attempt.
func (o Output) Validate() error {
	if !o.Outcome.Valid() {
		return errors.New("planning: invalid output outcome")
	}
	if err := validateAttempts(o.Attempts); err != nil {
		return err
	}
	attempts := uint64(len(o.Attempts))
	passes := uint64(o.PlanningPasses)
	switch o.Outcome {
	case OutcomeAchieved:
		if passes != attempts {
			return errors.New("planning: achieved output requires one planning pass per attempt")
		}
	case OutcomeUnreachable:
		if attempts != 0 || passes != 1 {
			return errors.New("planning: unreachable output requires one initial planning pass and no attempts")
		}
	case OutcomeStuck:
		if attempts == 0 || passes != attempts && passes != attempts+1 {
			return errors.New("planning: stuck output requires attempted Actions and at most one final unsuccessful planning pass")
		}
	}
	return nil
}

func validateAttempts(attempts []Attempt) error {
	for index, attempt := range attempts {
		if err := attempt.Validate(); err != nil {
			return fmt.Errorf("planning: attempt %d: %w", index, err)
		}
	}
	return nil
}
