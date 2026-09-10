package agent

import (
	"errors"
	"fmt"
)

// ErrResourceLimitExceeded reports a designed execution bound, not an Engine defect.
var ErrResourceLimitExceeded = errors.New("agent: resource limit exceeded")

const (
	defaultMaxSteps          uint64 = 10_000
	defaultMaxEffects        uint64 = 10_000
	defaultMaxSignals        uint64 = 100_000
	defaultMaxPendingSignals uint64 = 10_000
	defaultMaxDepth          uint32 = 16
	defaultMaxChildren       uint32 = 64
	defaultMaxActiveChildren uint32 = 16
	defaultMaxTreeProcesses  uint32 = 1024
)

// Limits bounds Framework-owned execution growth. Zero-valued fields in
// EngineConfig inherit DefaultLimits; ProcessSnapshot stores effective non-zero values
// so restoration preserves the same execution contract.
type Limits struct {
	// MaxSteps bounds committed Steps.
	MaxSteps uint64 `json:"max_steps"`

	// MaxEffects bounds Effects prepared across all Steps.
	MaxEffects uint64 `json:"max_effects"`

	// MaxSignals bounds all accepted external and Engine-generated Signals.
	MaxSignals uint64 `json:"max_signals"`

	// MaxPendingSignals bounds the current unconsumed mailbox suffix and the
	// suffix after the prepared Step consumes inputs and appends settlements.
	// Every arriving Signal preserves both bounds regardless of its source.
	MaxPendingSignals uint64 `json:"max_pending_signals"`
}

// DefaultLimits returns conservative hard bounds for one Process.
func DefaultLimits() Limits {
	return Limits{
		MaxSteps: defaultMaxSteps, MaxEffects: defaultMaxEffects,
		MaxSignals: defaultMaxSignals, MaxPendingSignals: defaultMaxPendingSignals,
	}
}

func (l Limits) Valid() bool {
	return l.validate() == nil
}

func (l Limits) resolve() (Limits, error) {
	effective := l
	defaults := DefaultLimits()
	if effective.MaxSteps == 0 {
		effective.MaxSteps = defaults.MaxSteps
	}
	if effective.MaxEffects == 0 {
		effective.MaxEffects = defaults.MaxEffects
	}
	if effective.MaxSignals == 0 {
		effective.MaxSignals = defaults.MaxSignals
	}
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
	case l.MaxSteps == 0:
		return errors.New("MaxSteps must be greater than zero")
	case l.MaxEffects == 0:
		return errors.New("MaxEffects must be greater than zero")
	case l.MaxSignals == 0:
		return errors.New("MaxSignals must be greater than zero")
	case l.MaxPendingSignals == 0:
		return errors.New("MaxPendingSignals must be greater than zero")
	case l.MaxPendingSignals > l.MaxSignals:
		return fmt.Errorf("MaxPendingSignals (%d) exceeds MaxSignals (%d)", l.MaxPendingSignals, l.MaxSignals)
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

func (u Usage) validFor(limits Limits) bool {
	return u.CommittedSteps <= limits.MaxSteps &&
		u.PreparedEffects <= limits.MaxEffects &&
		u.AcceptedSignals <= limits.MaxSignals
}

// Budget is a non-renewable allocation of Framework-owned work units. A child
// allocation is permanently transferred from its parent's remaining budget;
// unused units are not silently reclaimed or duplicated. Remaining budget
// excludes the parent's prepared Step and its future settlement Signals.
// Construct allocations with named fields; child admission and capability
// constructors validate the complete allocation before it grants authority.
type Budget struct {
	// Steps is the maximum committed Step count allocated to the child.
	Steps uint64 `json:"steps"`
	// Effects is the maximum prepared Effect count allocated to the child.
	Effects uint64 `json:"effects"`
	// Signals is the maximum accepted Signal count allocated to the child.
	Signals uint64 `json:"signals"`
}

func (b Budget) Valid() bool {
	return b.Steps > 0 && b.Effects > 0 && b.Signals > 0
}

func budgetFromLimits(limits Limits) Budget {
	return Budget{Steps: limits.MaxSteps, Effects: limits.MaxEffects, Signals: limits.MaxSignals}
}

func limitsFromBudget(parent Limits, budget Budget) (Limits, error) {
	if !budget.Valid() {
		return Limits{}, ErrResourceLimitExceeded
	}
	pending := min(parent.MaxPendingSignals, budget.Signals)
	limits := Limits{
		MaxSteps: budget.Steps, MaxEffects: budget.Effects,
		MaxSignals: budget.Signals, MaxPendingSignals: pending,
	}
	if !limits.Valid() {
		return Limits{}, fmt.Errorf("%w: child budget cannot form valid limits", ErrResourceLimitExceeded)
	}
	return limits, nil
}

func (b Budget) contains(usage Usage, reserved Budget) bool {
	return resourceQuantitiesFit(b.Steps, usage.CommittedSteps, reserved.Steps) &&
		resourceQuantitiesFit(b.Effects, usage.PreparedEffects, reserved.Effects) &&
		resourceQuantitiesFit(b.Signals, usage.AcceptedSignals, reserved.Signals)
}

func (b Budget) canAllocate(usage Usage, reserved, requested Budget) bool {
	return requested.Valid() &&
		resourceQuantitiesFit(b.Steps, usage.CommittedSteps, reserved.Steps, requested.Steps) &&
		resourceQuantitiesFit(b.Effects, usage.PreparedEffects, reserved.Effects, requested.Effects) &&
		resourceQuantitiesFit(b.Signals, usage.AcceptedSignals, reserved.Signals, requested.Signals)
}

func (b Budget) add(other Budget) (Budget, bool) {
	const maxUint64 = ^uint64(0)
	if !resourceQuantitiesFit(maxUint64, b.Steps, other.Steps) ||
		!resourceQuantitiesFit(maxUint64, b.Effects, other.Effects) ||
		!resourceQuantitiesFit(maxUint64, b.Signals, other.Signals) {
		return Budget{}, false
	}
	result := Budget{
		Steps: b.Steps + other.Steps, Effects: b.Effects + other.Effects,
		Signals: b.Signals + other.Signals,
	}
	return result, true
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
// limits. Every zero field in EngineConfig inherits DefaultTreeLimits.
type TreeLimits struct {
	// MaxDepth bounds the root-relative depth of any Process.
	MaxDepth uint32 `json:"max_depth"`
	// MaxChildren bounds the lifetime child count of one Process.
	MaxChildren uint32 `json:"max_children"`
	// MaxActiveChildren bounds concurrent non-terminal children of one Process.
	MaxActiveChildren uint32 `json:"max_active_children"`
	// MaxTreeProcesses bounds the lifetime Process count of one tree.
	MaxTreeProcesses uint32 `json:"max_tree_processes"`
}

// DefaultTreeLimits returns conservative structured-concurrency bounds.
func DefaultTreeLimits() TreeLimits {
	return TreeLimits{
		MaxDepth: defaultMaxDepth, MaxChildren: defaultMaxChildren,
		MaxActiveChildren: defaultMaxActiveChildren, MaxTreeProcesses: defaultMaxTreeProcesses,
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
	if effective.MaxChildren == 0 {
		effective.MaxChildren = defaults.MaxChildren
	}
	if effective.MaxActiveChildren == 0 {
		effective.MaxActiveChildren = defaults.MaxActiveChildren
	}
	if effective.MaxTreeProcesses == 0 {
		effective.MaxTreeProcesses = defaults.MaxTreeProcesses
	}
	if err := effective.validate(); err != nil {
		return TreeLimits{}, fmt.Errorf("%w: tree limits: %w", ErrInvalidEngineConfig, err)
	}
	return effective, nil
}

func (t TreeLimits) validate() error {
	switch {
	case t.MaxDepth == 0:
		return errors.New("MaxDepth must be greater than zero")
	case t.MaxChildren == 0:
		return errors.New("MaxChildren must be greater than zero")
	case t.MaxActiveChildren == 0:
		return errors.New("MaxActiveChildren must be greater than zero")
	case t.MaxActiveChildren > t.MaxChildren:
		return fmt.Errorf("MaxActiveChildren (%d) exceeds MaxChildren (%d)", t.MaxActiveChildren, t.MaxChildren)
	case t.MaxTreeProcesses == 0:
		return errors.New("MaxTreeProcesses must be greater than zero")
	default:
		return nil
	}
}
