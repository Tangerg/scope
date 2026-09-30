package agent

import (
	"errors"
	"fmt"
	"math"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// ErrResourceLimitExceeded reports a designed execution bound, not an Engine defect.
var ErrResourceLimitExceeded = errors.New("agent: resource limit exceeded")

// ErrCounterExhausted reports exhausted numeric identity or accounting space,
// independently of the Host's quota policy. Work stops before a counter wraps.
// Strategies exhausting their own counters report this same sentinel.
var ErrCounterExhausted = NewClassifiedError(
	FailureKindExecution, failureCodeEngineCounterExhausted, "agent: counter exhausted",
)

const (
	defaultMaxPendingSignals uint64 = 10_000
	defaultMaxDepth          uint32 = 16
	defaultMaxActiveChildren uint32 = 16
)

// Usage contains monotonic Framework-owned counters. It deliberately excludes
// provider pricing and Strategy-specific concepts such as tokens or tool calls.
type Usage struct {
	// CommittedSteps counts finalized Steps.
	CommittedSteps uint64 `json:"committed_steps"`

	// PreparedEffects counts stable logical Effect identities, not replay attempts.
	PreparedEffects uint64 `json:"prepared_effects"`

	// AcceptedSignals counts external and Engine-generated mailbox entries.
	AcceptedSignals uint64 `json:"accepted_signals"`

	// DroppedDeltas counts increments rejected by validation or the bounded queue.
	DroppedDeltas uint64 `json:"dropped_deltas"`
}

// Budget grants cumulative work authority. Zero quotas are unlimited. A finite
// parent permanently charges child grants, including unused child work. An
// unlimited parent grants either kind without a finite debit. Each dimension
// is independent; a finite parent cannot grant an unlimited child quota.
type Budget struct {
	// Steps bounds committed Steps. A finite zero forbids new computation.
	Steps Quota `json:"steps"`
	// Effects bounds stable Effect identities prepared across all Steps. A
	// finite zero permits pure Steps but forbids Effects.
	Effects Quota `json:"effects"`
	// Signals bounds accepted external and Engine-generated Signals. A finite
	// zero permits computations that need no runtime input or settlements.
	Signals Quota `json:"signals"`
}

func (b *Budget) UnmarshalJSON(data []byte) error {
	if b == nil {
		return errors.New("agent: nil budget receiver")
	}
	type wire Budget
	value, err := jsonwire.Decode[wire](data, "steps", "effects", "signals")
	if err != nil {
		return err
	}
	*b = Budget(value)
	return nil
}

func (b Budget) contains(usage Usage, allocated resourceAmounts) bool {
	return (b.Steps.limited || allocated.Steps == 0) &&
		(b.Effects.limited || allocated.Effects == 0) &&
		(b.Signals.limited || allocated.Signals == 0) &&
		b.Steps.Allows(usage.CommittedSteps, allocated.Steps) &&
		b.Effects.Allows(usage.PreparedEffects, allocated.Effects) &&
		b.Signals.Allows(usage.AcceptedSignals, allocated.Signals)
}

func (b Budget) allocation(requested Budget) (resourceAmounts, bool) {
	steps, stepsOK := b.Steps.allocation(requested.Steps)
	effects, effectsOK := b.Effects.allocation(requested.Effects)
	signals, signalsOK := b.Signals.allocation(requested.Signals)
	return resourceAmounts{Steps: steps, Effects: effects, Signals: signals}, stepsOK && effectsOK && signalsOK
}

func (b Budget) canAllocate(usage Usage, reserved resourceAmounts, requested Budget) bool {
	debit, ok := b.allocation(requested)
	if !ok {
		return false
	}
	return b.Steps.Allows(usage.CommittedSteps, reserved.Steps, debit.Steps) &&
		b.Effects.Allows(usage.PreparedEffects, reserved.Effects, debit.Effects) &&
		b.Signals.Allows(usage.AcceptedSignals, reserved.Signals, debit.Signals)
}

// Only finite parent dimensions carry debits. This makes rollback exact even
// when several children hold unlimited grants in other dimensions.
type resourceAmounts struct {
	Steps   uint64 `json:"steps"`
	Effects uint64 `json:"effects"`
	Signals uint64 `json:"signals"`
}

func (r resourceAmounts) add(other resourceAmounts) (resourceAmounts, bool) {
	if !resourceQuantitiesFit(math.MaxUint64, r.Steps, other.Steps) ||
		!resourceQuantitiesFit(math.MaxUint64, r.Effects, other.Effects) ||
		!resourceQuantitiesFit(math.MaxUint64, r.Signals, other.Signals) {
		return resourceAmounts{}, false
	}
	return resourceAmounts{Steps: r.Steps + other.Steps, Effects: r.Effects + other.Effects, Signals: r.Signals + other.Signals}, true
}

func resourceQuantitiesFit(limit uint64, quantities ...uint64) bool {
	remaining := limit
	for _, quantity := range quantities {
		if quantity > remaining {
			return false
		}
		remaining -= quantity
	}
	return true
}

func saturatingCountAdd(value, increment uint64) uint64 {
	if increment > math.MaxUint64-value {
		return math.MaxUint64
	}
	return value + increment
}

// TreeLimits is the capacity policy shared by every Process of one root tree.
// Cumulative counts and snapshot bytes default to unlimited; zero depth,
// active-child capacity, and pending-Signal capacity inherit DefaultTreeLimits.
// A tree snapshot carries exactly one TreeLimits, so recovery cannot give
// members different bounds.
type TreeLimits struct {
	// MaxSnapshotBytes bounds the encoded tree, including completed descendants.
	// Admission includes each live Process's lifecycle and Framework reservations.
	// The per-Process overhead described by MaxProcessSnapshotBytes accumulates
	// across live members; terminal members contribute only their encoded size.
	// A finite zero denies every new tree snapshot admission.
	MaxSnapshotBytes Quota `json:"max_snapshot_bytes"`

	// MaxProcessSnapshotBytes bounds each encoded Process snapshot, including
	// retained history. Admission of a live Process also reserves worst-case
	// control, termination, diagnostic, and Framework settlement growth; control
	// strings alone reserve about 144 KiB after JSON escaping. Terminal Processes
	// need only their encoded size. A finite zero denies every new admission.
	MaxProcessSnapshotBytes Quota `json:"max_process_snapshot_bytes"`

	// MaxPendingSignals bounds each Process's unconsumed mailbox suffix and the
	// suffix after its prepared Step consumes inputs and appends settlements.
	// Every arriving Signal preserves both bounds regardless of its source.
	// Capacity must fit the uint32 consumed-Signal count in one Transition.
	MaxPendingSignals uint64 `json:"max_pending_signals"`

	// MaxDepth bounds the root-relative depth of any Process.
	MaxDepth uint32 `json:"max_depth"`
	// MaxChildren bounds the lifetime child count of one Process. A finite
	// zero confines new execution to the root.
	MaxChildren Quota `json:"max_children"`
	// MaxActiveChildren bounds concurrent non-terminal children of one Process.
	MaxActiveChildren uint32 `json:"max_active_children"`
	// MaxTreeProcesses bounds the lifetime Process count of one tree. A finite
	// zero is rejected because a tree includes its root.
	MaxTreeProcesses Quota `json:"max_tree_processes"`
}

func (t *TreeLimits) UnmarshalJSON(data []byte) error {
	if t == nil {
		return errors.New("agent: nil tree limits receiver")
	}
	type wire TreeLimits
	value, err := jsonwire.Decode[wire](data,
		"max_snapshot_bytes", "max_process_snapshot_bytes", "max_pending_signals",
		"max_depth", "max_children", "max_active_children", "max_tree_processes",
	)
	if err != nil {
		return err
	}
	*t = TreeLimits(value)
	return nil
}

// DefaultTreeLimits returns conservative structured-concurrency and mailbox
// bounds; cumulative counts and snapshot bytes remain unlimited.
func DefaultTreeLimits() TreeLimits {
	return TreeLimits{
		MaxPendingSignals: defaultMaxPendingSignals,
		MaxDepth:          defaultMaxDepth,
		MaxActiveChildren: defaultMaxActiveChildren,
	}
}

func (t TreeLimits) Valid() bool {
	return t.validate() == nil
}

func (t TreeLimits) resolve() (TreeLimits, error) {
	effective := t
	defaults := DefaultTreeLimits()
	if effective.MaxPendingSignals == 0 {
		effective.MaxPendingSignals = defaults.MaxPendingSignals
	}
	if effective.MaxDepth == 0 {
		effective.MaxDepth = defaults.MaxDepth
	}
	if effective.MaxActiveChildren == 0 {
		effective.MaxActiveChildren = defaults.MaxActiveChildren
	}
	if err := effective.validate(); err != nil {
		return TreeLimits{}, fmt.Errorf("%w: tree limits: %w", ErrInvalidEngineConfig, err)
	}
	return effective, nil
}

func (t TreeLimits) validate() error {
	switch {
	case !t.MaxTreeProcesses.Allows(1):
		return errors.New("MaxTreeProcesses must admit the root Process")
	case t.MaxPendingSignals == 0:
		return errors.New("MaxPendingSignals must be greater than zero")
	case t.MaxPendingSignals > math.MaxUint32:
		return errors.New("MaxPendingSignals exceeds the representable Transition capacity")
	case t.MaxDepth == 0:
		return errors.New("MaxDepth must be greater than zero")
	case t.MaxActiveChildren == 0:
		return errors.New("MaxActiveChildren must be greater than zero")
	default:
		return nil
	}
}

// admitsDepth reports whether a Process at depth may exist in this tree.
func (t TreeLimits) admitsDepth(depth uint32) bool { return depth <= t.MaxDepth }

// Only counters without an authoritative lifecycle record are stored separately.
// PreparedEffects counts identities independently of quota policy; overflow is an error.
// DroppedDeltas is best-effort telemetry and saturates instead of stopping work.
type processCounters struct {
	PreparedEffects uint64 `json:"prepared_effects"`
	DroppedDeltas   uint64 `json:"dropped_deltas"`
}

// usage combines the stored counters with the two counts whose lifecycle
// records own them: committed Steps and admitted mailbox entries.
func (p processCounters) usage(committedSteps, acceptedSignals uint64) Usage {
	return Usage{
		CommittedSteps: committedSteps, AcceptedSignals: acceptedSignals,
		PreparedEffects: p.PreparedEffects, DroppedDeltas: p.DroppedDeltas,
	}
}

func (p *processCounters) UnmarshalJSON(data []byte) error {
	if p == nil {
		return errors.New("agent: nil process counters receiver")
	}
	type wire processCounters
	value, err := jsonwire.Decode[wire](data, "prepared_effects", "dropped_deltas")
	if err != nil {
		return err
	}
	*p = processCounters(value)
	return nil
}
