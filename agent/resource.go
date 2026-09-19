package agent

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// ErrResourceLimitExceeded reports a designed execution bound, not an Engine defect.
var ErrResourceLimitExceeded = errors.New("agent: resource limit exceeded")

// ErrCounterExhausted reports exhausted numeric identity or accounting space,
// independently of the Host's quota policy. Work stops before a counter wraps.
var ErrCounterExhausted = errors.New("agent: counter exhausted")

const (
	defaultMaxPendingSignals uint64 = 10_000
	defaultMaxDepth          uint32 = 16
	defaultMaxActiveChildren uint32 = 16
)

// Limits separates cumulative work quotas from current mailbox capacity.
// Zero quotas are unlimited. Only MaxPendingSignals inherits a finite default.
// Snapshots retain the effective contract independently of Engine configuration.
type Limits struct {
	// MaxSnapshotBytes bounds the encoded Process snapshot, including retained
	// history. Admission also reserves mandatory control, termination, diagnostic,
	// and Framework settlement growth. A finite quota must fit that reservation,
	// even when the current encoding is smaller. With the current diagnostic and
	// reason bounds, control strings alone reserve 144 KiB per live Process after
	// worst-case JSON escaping; metadata, state, history, and Effects add to it.
	// The required capacity depends on the admitted state, not a fixed minimum.
	// Terminal Processes need only their encoded size. A finite zero denies every
	// new admission.
	MaxSnapshotBytes Quota `json:"max_snapshot_bytes"`

	// Budget bounds cumulative work and grants child allocations.
	Budget Budget `json:"budget"`

	// MaxPendingSignals bounds the current unconsumed mailbox suffix and the
	// suffix after the prepared Step consumes inputs and appends settlements.
	// Every arriving Signal preserves both bounds regardless of its source.
	// Capacity must fit the uint32 consumed-Signal count in one Transition.
	MaxPendingSignals uint64 `json:"max_pending_signals"`
}

func (l *Limits) UnmarshalJSON(data []byte) error {
	if l == nil {
		return errors.New("agent: nil limits receiver")
	}
	type wire Limits
	value, err := jsonwire.Decode[wire](data, "budget", "max_snapshot_bytes", "max_pending_signals")
	if err != nil {
		return err
	}
	*l = Limits(value)
	return nil
}

// DefaultLimits leaves cumulative work and snapshot size unlimited and bounds pending Signals.
func DefaultLimits() Limits {
	return Limits{
		MaxPendingSignals: defaultMaxPendingSignals,
	}
}

func (l Limits) Valid() bool {
	return l.validate() == nil
}

func (l Limits) resolve() (Limits, error) {
	effective := l
	defaults := DefaultLimits()
	if effective.MaxPendingSignals == 0 {
		effective.MaxPendingSignals = defaults.MaxPendingSignals
	}
	if err := effective.validate(); err != nil {
		return Limits{}, fmt.Errorf("%w: limits: %w", ErrInvalidEngineConfig, err)
	}
	return effective, nil
}

func (l Limits) validate() error {
	switch {
	case l.MaxPendingSignals == 0:
		return errors.New("MaxPendingSignals must be greater than zero")
	case l.MaxPendingSignals > uint64(^uint32(0)):
		return errors.New("MaxPendingSignals exceeds the representable Transition capacity")
	default:
		return nil
	}
}

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
	const maximum = ^uint64(0)
	if !resourceQuantitiesFit(maximum, r.Steps, other.Steps) ||
		!resourceQuantitiesFit(maximum, r.Effects, other.Effects) ||
		!resourceQuantitiesFit(maximum, r.Signals, other.Signals) {
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
	const maxUint64 = ^uint64(0)
	if increment > maxUint64-value {
		return maxUint64
	}
	return value + increment
}

// TreeLimits bounds structural expansion independently of per-Process work
// limits. Cumulative counts and snapshot bytes default to unlimited; zero
// depth and active-child capacity inherit DefaultTreeLimits.
type TreeLimits struct {
	// MaxSnapshotBytes bounds the encoded tree, including completed descendants.
	// Admission includes each live Process's lifecycle and Framework reservations.
	// The per-Process overhead described by Limits.MaxSnapshotBytes accumulates
	// across live members; terminal members contribute only their encoded size.
	// A finite zero denies every new tree snapshot admission.
	MaxSnapshotBytes Quota `json:"max_snapshot_bytes"`

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
	value, err := jsonwire.Decode[wire](data, "max_children", "max_tree_processes", "max_snapshot_bytes")
	if err != nil {
		return err
	}
	*t = TreeLimits(value)
	return nil
}

// DefaultTreeLimits returns conservative structured-concurrency bounds.
func DefaultTreeLimits() TreeLimits {
	return TreeLimits{
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
	case t.MaxDepth == 0:
		return errors.New("MaxDepth must be greater than zero")
	case t.MaxActiveChildren == 0:
		return errors.New("MaxActiveChildren must be greater than zero")
	default:
		return nil
	}
}

// Only counters without an authoritative lifecycle record are stored separately.
// PreparedEffects counts identities independently of quota policy; overflow is an error.
// DroppedDeltas is best-effort telemetry and saturates instead of stopping work.
type processCounters struct {
	PreparedEffects uint64 `json:"prepared_effects"`
	DroppedDeltas   uint64 `json:"dropped_deltas"`
}
