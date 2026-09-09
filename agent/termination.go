package agent

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const maxTerminationReasonBytes = 4096

var errInvalidTermination = errors.New("agent: invalid termination")

// deadlineOwner identifies the lifecycle boundary whose reached deadline is
// recorded by the Engine.
type deadlineOwner string

const (
	deadlineOwnerInvalid deadlineOwner = ""
	deadlineOwnerProcess deadlineOwner = "process"
	deadlineOwnerParent  deadlineOwner = "parent"
	deadlineOwnerHost    deadlineOwner = "host"
)

func (d deadlineOwner) valid() bool {
	return d == deadlineOwnerProcess || d == deadlineOwnerParent || d == deadlineOwnerHost
}

func (d deadlineOwner) String() string {
	if !d.valid() {
		return invalidEnumName
	}
	return string(d)
}

// cancellationOwner identifies a non-deadline cancellation source.
type cancellationOwner string

const (
	cancellationOwnerInvalid cancellationOwner = ""
	cancellationOwnerParent  cancellationOwner = "parent"
	cancellationOwnerHost    cancellationOwner = "host"
)

func (c cancellationOwner) valid() bool {
	return c == cancellationOwnerParent || c == cancellationOwnerHost
}

func (c cancellationOwner) String() string {
	if !c.valid() {
		return invalidEnumName
	}
	return string(c)
}

// killIntent records an explicit Engine kill request.
type killIntent struct{ reason string }

func newKillIntent(reason string) (killIntent, error) {
	if err := validateTerminationReason(reason); err != nil {
		return killIntent{}, err
	}
	return killIntent{reason: reason}, nil
}

func (k killIntent) valid() bool { return k.reason != "" }

// deadlineIntent records that a specific Process lifecycle deadline was
// reached. A local Effect timeout remains a settlement Signal unless promoted
// to a Process termination before constructing these facts.
type deadlineIntent struct {
	owner  deadlineOwner
	reason string
}

func newDeadlineIntent(owner deadlineOwner, reason string) (deadlineIntent, error) {
	if !owner.valid() {
		return deadlineIntent{}, fmt.Errorf("%w: invalid deadline owner", errInvalidTermination)
	}
	if err := validateTerminationReason(reason); err != nil {
		return deadlineIntent{}, err
	}
	return deadlineIntent{owner: owner, reason: reason}, nil
}

func (d deadlineIntent) valid() bool {
	return d.owner.valid() && d.reason != ""
}

// cancellationIntent records a non-deadline cancellation from a parent Process
// or Host context.
type cancellationIntent struct {
	owner  cancellationOwner
	reason string
}

func newCancellationIntent(owner cancellationOwner, reason string) (cancellationIntent, error) {
	if !owner.valid() {
		return cancellationIntent{}, fmt.Errorf("%w: invalid cancellation owner", errInvalidTermination)
	}
	if err := validateTerminationReason(reason); err != nil {
		return cancellationIntent{}, err
	}
	return cancellationIntent{owner: owner, reason: reason}, nil
}

func (c cancellationIntent) valid() bool {
	return c.owner.valid() && c.reason != ""
}

// stepOutcomeKind describes the valid terminal result of a Step. A zero outcome
// is allowed only when control facts independently terminate a Process.
type stepOutcomeKind uint8

const (
	stepOutcomeNone stepOutcomeKind = iota
	stepOutcomeCompleted
	stepOutcomeFailed
)

// stepOutcome carries either legal completion or a classified failure.
type stepOutcome struct {
	kind    stepOutcomeKind
	failure Failure
}

func completedOutcome() stepOutcome { return stepOutcome{kind: stepOutcomeCompleted} }

func failedOutcome(failure Failure) (stepOutcome, error) {
	if !failure.Valid() {
		return stepOutcome{}, fmt.Errorf("%w: failure: %w", errInvalidTermination, ErrInvalidFailure)
	}
	return stepOutcome{kind: stepOutcomeFailed, failure: failure}, nil
}

func (s stepOutcome) valid() bool {
	return s.kind == stepOutcomeNone && !s.failure.Valid() ||
		s.kind == stepOutcomeCompleted && !s.failure.Valid() ||
		s.kind == stepOutcomeFailed && s.failure.Valid()
}

// terminationFacts are the independently recorded facts used by the Engine's
// terminal priority matrix. Presence is expressed by validated value objects,
// not by interpreting an error such as context.Canceled.
type terminationFacts struct {
	kill         killIntent
	deadline     deadlineIntent
	cancellation cancellationIntent
	outcome      stepOutcome
}

// TerminationCause is the stable reason category of a terminal Process.
type TerminationCause string

const (
	// TerminationCauseInvalid is the invalid zero value.
	TerminationCauseInvalid TerminationCause = ""
	// TerminationCauseCompletion identifies successful semantic completion.
	TerminationCauseCompletion TerminationCause = "completion"
	// TerminationCauseEngineKill identifies an explicit Engine kill.
	TerminationCauseEngineKill TerminationCause = "engine_kill"
	// TerminationCauseProcessDeadline identifies the Process's own deadline.
	TerminationCauseProcessDeadline TerminationCause = "process_deadline"
	// TerminationCauseParentDeadline identifies deadline propagation from a parent.
	TerminationCauseParentDeadline TerminationCause = "parent_deadline"
	// TerminationCauseHostDeadline identifies expiry of the Host context.
	TerminationCauseHostDeadline TerminationCause = "host_deadline"
	// TerminationCauseParentCancellation identifies cancellation by a parent Process.
	TerminationCauseParentCancellation TerminationCause = "parent_cancellation"
	// TerminationCauseHostCancellation identifies cancellation by the Host context.
	TerminationCauseHostCancellation TerminationCause = "host_cancellation"
	// TerminationCauseExecutionFailure identifies an ordinary Strategy failure.
	TerminationCauseExecutionFailure TerminationCause = "execution_failure"
	// TerminationCauseContractFailure identifies a contract violation.
	TerminationCauseContractFailure TerminationCause = "contract_failure"
	// TerminationCauseExternalFailure identifies failed external infrastructure.
	TerminationCauseExternalFailure TerminationCause = "external_failure"
	// TerminationCausePanic identifies a recovered execution-boundary panic.
	TerminationCausePanic TerminationCause = "panic"
)

func (t TerminationCause) Valid() bool {
	switch t {
	case TerminationCauseCompletion, TerminationCauseEngineKill,
		TerminationCauseProcessDeadline, TerminationCauseParentDeadline,
		TerminationCauseHostDeadline, TerminationCauseParentCancellation,
		TerminationCauseHostCancellation, TerminationCauseExecutionFailure,
		TerminationCauseContractFailure, TerminationCauseExternalFailure,
		TerminationCausePanic:
		return true
	default:
		return false
	}
}

func (t TerminationCause) String() string {
	if !t.Valid() {
		return invalidEnumName
	}
	return string(t)
}

// Termination is the immutable result of applying the terminal priority matrix.
type Termination struct {
	status              Status
	cause               TerminationCause
	reason              string
	failure             Failure
	unresolvedEffectIDs []EffectID
}

// resolveTermination applies Engine kill, deadline, cancellation, and Step
// outcome facts in that priority order. It never infers intent from an error.
func resolveTermination(facts terminationFacts) (Termination, error) {
	if !facts.outcome.valid() {
		return Termination{}, fmt.Errorf("%w: invalid Step outcome", errInvalidTermination)
	}
	if facts.kill.valid() {
		return Termination{status: StatusKilled, cause: TerminationCauseEngineKill, reason: facts.kill.reason}, nil
	}
	if facts.deadline.valid() {
		return terminationForDeadline(facts.deadline), nil
	}
	if facts.cancellation.valid() {
		return terminationForCancellation(facts.cancellation), nil
	}
	switch facts.outcome.kind {
	case stepOutcomeCompleted:
		return Termination{status: StatusCompleted, cause: TerminationCauseCompletion}, nil
	case stepOutcomeFailed:
		return terminationForFailure(facts.outcome.failure), nil
	default:
		return Termination{}, fmt.Errorf("%w: no terminal fact was recorded", errInvalidTermination)
	}
}

func terminationForDeadline(intent deadlineIntent) Termination {
	cause := TerminationCauseProcessDeadline
	switch intent.owner {
	case deadlineOwnerParent:
		cause = TerminationCauseParentDeadline
	case deadlineOwnerHost:
		cause = TerminationCauseHostDeadline
	}
	return Termination{status: StatusTimedOut, cause: cause, reason: intent.reason}
}

func terminationForCancellation(intent cancellationIntent) Termination {
	cause := TerminationCauseParentCancellation
	if intent.owner == cancellationOwnerHost {
		cause = TerminationCauseHostCancellation
	}
	return Termination{status: StatusCanceled, cause: cause, reason: intent.reason}
}

func terminationForFailure(failure Failure) Termination {
	cause := TerminationCauseExecutionFailure
	switch failure.Kind() {
	case FailureKindContract:
		cause = TerminationCauseContractFailure
	case FailureKindExternal:
		cause = TerminationCauseExternalFailure
	case FailureKindPanic:
		cause = TerminationCausePanic
	}
	return Termination{status: StatusFailed, cause: cause, reason: failure.Message(), failure: failure}
}

func validateTerminationReason(reason string) error {
	if reason == "" || strings.TrimSpace(reason) != reason || !utf8.ValidString(reason) || len(reason) > maxTerminationReasonBytes {
		return fmt.Errorf("%w: reason must be non-empty, trimmed UTF-8 within %d bytes", errInvalidTermination, maxTerminationReasonBytes)
	}
	return nil
}

// Status returns the resolved terminal Process status.
func (t Termination) Status() Status { return t.status }

// Cause returns the stable machine-readable terminal category.
func (t Termination) Cause() TerminationCause { return t.cause }

// Reason returns a bounded diagnostic reason. Completion has an empty reason.
func (t Termination) Reason() string { return t.reason }

// Failure returns the classified failure for StatusFailed.
func (t Termination) Failure() (Failure, bool) {
	return t.failure, t.status == StatusFailed
}

// UnresolvedEffectIDs returns the canonical identities of external operations
// that may have occurred but were not durably resolved when the tree stopped.
func (t Termination) UnresolvedEffectIDs() []EffectID {
	return slices.Clone(t.unresolvedEffectIDs)
}

func (t Termination) withUnresolvedEffectIDs(effectIDs []EffectID) Termination {
	t.unresolvedEffectIDs = canonicalEffectIDs(effectIDs)
	return t
}

func canonicalEffectIDs(effectIDs []EffectID) []EffectID {
	canonical := slices.Clone(effectIDs)
	slices.SortFunc(canonical, func(left, right EffectID) int {
		return cmp.Compare(left.String(), right.String())
	})
	return slices.Compact(canonical)
}

func (t Termination) Valid() bool {
	if !t.status.Terminal() || !t.cause.Valid() {
		return false
	}
	if t.status != StatusCompleted && validateTerminationReason(t.reason) != nil {
		return false
	}
	for index, effectID := range t.unresolvedEffectIDs {
		if !effectID.Valid() || index > 0 &&
			t.unresolvedEffectIDs[index-1].String() >= effectID.String() {
			return false
		}
	}
	switch t.status {
	case StatusCompleted:
		return t.cause == TerminationCauseCompletion && t.reason == "" &&
			!t.failure.Valid() && len(t.unresolvedEffectIDs) == 0
	case StatusFailed:
		if !t.failure.Valid() || t.reason != t.failure.Message() {
			return false
		}
		return t.cause == terminationForFailure(t.failure).cause
	case StatusCanceled:
		return (t.cause == TerminationCauseParentCancellation || t.cause == TerminationCauseHostCancellation) &&
			!t.failure.Valid()
	case StatusTimedOut:
		return (t.cause == TerminationCauseProcessDeadline || t.cause == TerminationCauseParentDeadline || t.cause == TerminationCauseHostDeadline) &&
			!t.failure.Valid()
	case StatusKilled:
		return t.cause == TerminationCauseEngineKill && !t.failure.Valid()
	default:
		return false
	}
}

func (t Termination) MarshalJSON() ([]byte, error) {
	if !t.Valid() {
		return nil, errInvalidTermination
	}
	wire := terminationWire{
		Status:              t.status,
		Cause:               t.cause,
		Reason:              t.reason,
		UnresolvedEffectIDs: t.UnresolvedEffectIDs(),
	}
	if t.failure.Valid() {
		wire.Failure = &t.failure
	}
	return json.Marshal(wire)
}

func (t *Termination) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("%w: nil receiver", errInvalidTermination)
	}
	wire, err := wireJSON.decode[terminationWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", errInvalidTermination, err)
	}
	value := Termination{
		status: wire.Status, cause: wire.Cause, reason: wire.Reason,
		unresolvedEffectIDs: slices.Clone(wire.UnresolvedEffectIDs),
	}
	if wire.Failure != nil {
		value.failure = *wire.Failure
	}
	if !value.Valid() {
		return errInvalidTermination
	}
	*t = value
	return nil
}

type terminationWire struct {
	Status              Status           `json:"status"`
	Cause               TerminationCause `json:"cause"`
	Reason              string           `json:"reason,omitempty"`
	Failure             *Failure         `json:"failure,omitempty"`
	UnresolvedEffectIDs []EffectID       `json:"unresolved_effect_ids,omitempty"`
}

func (Termination) JSONSchemaAlias() any { return terminationWire{} }
