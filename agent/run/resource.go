package run

import (
	"errors"
	"fmt"
	"math"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// ErrResourceLimitExceeded reports a designed execution bound, not a Runtime defect.
var ErrResourceLimitExceeded = NewClassifiedError(FailureKindExecution, failureCodeRuntimeResourceLimitExceeded, "run: resource limit exceeded")

// ErrCounterExhausted reports exhausted numeric identity or accounting space,
// independently of the Host's quota policy. Work stops before a counter wraps.
// Strategies exhausting their own counters report this same sentinel.
var ErrCounterExhausted = NewClassifiedError(
	FailureKindExecution, failureCodeRuntimeCounterExhausted, "run: counter exhausted",
)

const (
	defaultMaxPendingMessages uint64 = 10_000
	defaultMaxDepth           uint32 = 16
	defaultMaxActiveChildren  uint32 = 16
)

// Counters are projections of committed records; transient delta delivery cannot
// change durable usage. Each decoded counter must be present, including zero.
type Usage struct {
	CommittedSteps   uint64 `json:"committed_steps" jsonwire:"required"`
	PreparedEffects  uint64 `json:"prepared_effects" jsonwire:"required"`
	AcceptedMessages uint64 `json:"accepted_messages" jsonwire:"required"`
}

func (u *Usage) UnmarshalJSON(data []byte) error {
	if u == nil {
		return errors.New("run: nil usage receiver")
	}
	type wire Usage
	value, err := jsonwire.Decode[wire](data)
	if err != nil {
		return err
	}
	*u = Usage(value)
	return nil
}

func (u Usage) add(other Usage) (Usage, error) {
	if !resourceQuantitiesFit(math.MaxUint64, u.CommittedSteps, other.CommittedSteps) ||
		!resourceQuantitiesFit(math.MaxUint64, u.PreparedEffects, other.PreparedEffects) ||
		!resourceQuantitiesFit(math.MaxUint64, u.AcceptedMessages, other.AcceptedMessages) {
		return Usage{}, ErrCounterExhausted
	}
	return Usage{
		CommittedSteps:   u.CommittedSteps + other.CommittedSteps,
		PreparedEffects:  u.PreparedEffects + other.PreparedEffects,
		AcceptedMessages: u.AcceptedMessages + other.AcceptedMessages,
	}, nil
}

// Live folding and historical totals share the same contribution rule, even
// after a process has been reaped from the active tree.
func recordUsage(record Record) (ProcessID, Usage) {
	switch value := record.(type) {
	case Stepped:
		return value.Process, Usage{CommittedSteps: 1}
	case EffectIntent:
		return value.ID.Process, Usage{PreparedEffects: 1}
	case SignalAdmitted:
		return value.Process, Usage{AcceptedMessages: 1}
	case EffectSettled:
		if value.Outcome.Status() != SettlementStatusUnknown {
			return value.ID.Process, Usage{AcceptedMessages: 1}
		}
	case Exited:
		if parent, child := value.Process.Parent(); child {
			return parent, Usage{AcceptedMessages: 1}
		}
	}
	return ProcessID{}, Usage{}
}

// Budget grants cumulative work authority. Zero quotas are unlimited. A finite
// parent permanently charges child grants, including unused child work. An
// unlimited parent grants either kind without a finite debit. Each dimension
// is independent; a finite parent cannot grant an unlimited child quota.
type Budget struct {
	Steps    Quota `json:"steps" jsonwire:"required"`
	Effects  Quota `json:"effects" jsonwire:"required"`
	Messages Quota `json:"messages" jsonwire:"required"`
}

func (b *Budget) UnmarshalJSON(data []byte) error {
	if b == nil {
		return errors.New("run: nil budget receiver")
	}
	type wire Budget
	value, err := jsonwire.Decode[wire](data)
	if err != nil {
		return err
	}
	*b = Budget(value)
	return nil
}

func (b Budget) allocation(requested Budget) (resourceAmounts, bool) {
	steps, stepsOK := b.Steps.allocation(requested.Steps)
	effects, effectsOK := b.Effects.allocation(requested.Effects)
	messages, messagesOK := b.Messages.allocation(requested.Messages)
	return resourceAmounts{Steps: steps, Effects: effects, Messages: messages}, stepsOK && effectsOK && messagesOK
}

func (b Budget) canAllocate(usage Usage, reserved resourceAmounts, requested Budget) bool {
	debit, ok := b.allocation(requested)
	if !ok {
		return false
	}
	return b.Steps.Allows(usage.CommittedSteps, reserved.Steps, debit.Steps) &&
		b.Effects.Allows(usage.PreparedEffects, reserved.Effects, debit.Effects) &&
		b.Messages.Allows(usage.AcceptedMessages, reserved.Messages, debit.Messages)
}

// Only finite parent dimensions carry debits. This makes rollback exact even
// when several children hold unlimited grants in other dimensions.
type resourceAmounts struct {
	Steps    uint64 `json:"steps"`
	Effects  uint64 `json:"effects"`
	Messages uint64 `json:"messages"`
}

func (r resourceAmounts) add(other resourceAmounts) (resourceAmounts, bool) {
	if !resourceQuantitiesFit(math.MaxUint64, r.Steps, other.Steps) ||
		!resourceQuantitiesFit(math.MaxUint64, r.Effects, other.Effects) ||
		!resourceQuantitiesFit(math.MaxUint64, r.Messages, other.Messages) {
		return resourceAmounts{}, false
	}
	return resourceAmounts{Steps: r.Steps + other.Steps, Effects: r.Effects + other.Effects, Messages: r.Messages + other.Messages}, true
}

func (r resourceAmounts) covers(other resourceAmounts) bool {
	return r.Steps >= other.Steps && r.Effects >= other.Effects && r.Messages >= other.Messages
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

// Root Started owns the resolved policy. Child records never repeat these
// limits. Message capacity reserves Completions and child Exits before dispatch,
// so admitted work can finish even while external Signals arrive.
type TreeLimits struct {
	MaxRecordBytes     Quota  `json:"max_record_bytes" jsonwire:"required"`
	MaxCheckpointBytes Quota  `json:"max_checkpoint_bytes" jsonwire:"required"`
	MaxPendingMessages uint64 `json:"max_pending_messages" jsonwire:"required"`
	MaxDepth           uint32 `json:"max_depth" jsonwire:"required"`
	MaxChildren        Quota  `json:"max_children" jsonwire:"required"`
	MaxActiveChildren  uint32 `json:"max_active_children" jsonwire:"required"`
	MaxTreeProcesses   Quota  `json:"max_tree_processes" jsonwire:"required"`
}

func (t *TreeLimits) UnmarshalJSON(data []byte) error {
	if t == nil {
		return errors.New("run: nil tree limits receiver")
	}
	type wire TreeLimits
	value, err := jsonwire.Decode[wire](data)
	if err != nil {
		return err
	}
	*t = TreeLimits(value)
	return nil
}

// DefaultTreeLimits returns conservative structured-concurrency and mailbox
// bounds; cumulative counts and record bytes remain unlimited.
func DefaultTreeLimits() TreeLimits {
	return TreeLimits{
		MaxPendingMessages: defaultMaxPendingMessages,
		MaxDepth:           defaultMaxDepth,
		MaxActiveChildren:  defaultMaxActiveChildren,
	}
}

func (t TreeLimits) Valid() bool {
	return t.validate() == nil
}

func (t TreeLimits) resolve() (TreeLimits, error) {
	effective := t
	defaults := DefaultTreeLimits()
	if effective.MaxPendingMessages == 0 {
		effective.MaxPendingMessages = defaults.MaxPendingMessages
	}
	if effective.MaxDepth == 0 {
		effective.MaxDepth = defaults.MaxDepth
	}
	if effective.MaxActiveChildren == 0 {
		effective.MaxActiveChildren = defaults.MaxActiveChildren
	}
	if err := effective.validate(); err != nil {
		return TreeLimits{}, fmt.Errorf("%w: tree limits: %w", ErrInvalidConfig, err)
	}
	return effective, nil
}

func (t TreeLimits) validate() error {
	switch {
	case !t.MaxTreeProcesses.Allows(1):
		return errors.New("MaxTreeProcesses must admit the root Process")
	case t.MaxPendingMessages == 0:
		return errors.New("MaxPendingMessages must be greater than zero")
	case t.MaxPendingMessages > math.MaxUint32:
		return errors.New("MaxPendingMessages exceeds the representable Transition capacity")
	case t.MaxDepth == 0:
		return errors.New("MaxDepth must be greater than zero")
	case t.MaxActiveChildren == 0:
		return errors.New("MaxActiveChildren must be greater than zero")
	default:
		return nil
	}
}

func (t TreeLimits) admitsDepth(depth uint32) bool { return depth <= t.MaxDepth }

func (t TreeLimits) admitsChildren(children, active uint64) bool {
	return t.MaxChildren.Allows(children) && active <= uint64(t.MaxActiveChildren)
}

func (t TreeLimits) admitsTreeSize(processes uint64) bool {
	return t.MaxTreeProcesses.Allows(processes)
}

// admitRecord bounds each encoded record, and checkpoints separately, because a
// record is the unit a Store must accept atomically. The enclosing Entry or
// Batch has already admitted its structure and membership.
func (t TreeLimits) admitRecord(record Record) error {
	_, checkpoint := record.(Checkpoint)
	if bounded := t.MaxRecordBytes.limited || checkpoint && t.MaxCheckpointBytes.limited; !bounded {
		return nil
	}
	_, encoded, err := encodeRecord(record)
	if err != nil {
		return err
	}
	size := uint64(len(encoded))
	if !t.MaxRecordBytes.Allows(size) || checkpoint && !t.MaxCheckpointBytes.Allows(size) {
		return ErrResourceLimitExceeded
	}
	return nil
}
