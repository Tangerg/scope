package agent

import (
	"cmp"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidChildWait = errors.New("agent: invalid child wait")

// ChildWaitCondition identifies when a set of child Processes releases its
// parent. It counts children at the requested boundary; it does not cancel
// unfinished children or reinterpret child terminal statuses.
type ChildWaitCondition struct {
	kind   childWaitKind
	quorum uint32
}

type childWaitKind string

const (
	childWaitAll    childWaitKind = "all"
	childWaitAny    childWaitKind = "any"
	childWaitQuorum childWaitKind = "quorum"
)

func AllChildren() ChildWaitCondition { return ChildWaitCondition{kind: childWaitAll} }

func AnyChild() ChildWaitCondition { return ChildWaitCondition{kind: childWaitAny} }

func ChildQuorum(count uint32) (ChildWaitCondition, error) {
	condition := ChildWaitCondition{kind: childWaitQuorum, quorum: count}
	if count == 0 {
		return ChildWaitCondition{}, ErrInvalidChildWait
	}
	return condition, nil
}

func (c ChildWaitCondition) Valid() bool {
	return c.kind == childWaitAll && c.quorum == 0 ||
		c.kind == childWaitAny && c.quorum == 0 ||
		c.kind == childWaitQuorum && c.quorum > 0
}

// ChildWaitBoundary selects the lifecycle fact counted by a child wait.
type ChildWaitBoundary string

const (
	ChildWaitBoundaryInvalid ChildWaitBoundary = ""
	ChildWaitBoundaryResult  ChildWaitBoundary = "terminal_result"
	ChildWaitBoundaryDrained ChildWaitBoundary = "subtree_drained"
)

func (c ChildWaitBoundary) Valid() bool {
	switch c {
	case ChildWaitBoundaryResult, ChildWaitBoundaryDrained:
		return true
	default:
		return false
	}
}

func (c ChildWaitBoundary) String() string {
	if !c.Valid() {
		return invalidEnumName
	}
	return string(c)
}

type ChildWaitSpec struct {
	// Key is the Execution-owned logical identity of this wait request.
	Key WaitKey
	// Children lists direct child identities in result order.
	Children []ProcessID
	// Boundary explicitly selects terminal results or joined subtrees. A joined
	// subtree has completed its owned local work and required acknowledgments in
	// this runtime, as defined by Process.Join; remote uncertainty may remain.
	Boundary  ChildWaitBoundary
	Condition ChildWaitCondition
}

func (c ChildWaitSpec) Valid() bool {
	if !c.Key.Valid() || !c.Boundary.Valid() {
		return false
	}
	if !c.Condition.Valid() || len(c.Children) == 0 || uint64(len(c.Children)) > math.MaxUint32 || c.required() > uint32(len(c.Children)) {
		return false
	}
	seen := make(map[ProcessID]struct{}, len(c.Children))
	for _, childID := range c.Children {
		if !childID.Valid() {
			return false
		}
		if _, duplicate := seen[childID]; duplicate {
			return false
		}
		seen[childID] = struct{}{}
	}
	return true
}

func (c ChildWaitSpec) validateRelations(parent ProcessID, relation func(ProcessID) ProcessRelation) error {
	for _, id := range c.Children {
		actualParent, child := relation(id).ParentID()
		if !child || actualParent != parent {
			return ErrInvalidChildWait
		}
	}
	return nil
}

// required is total; Valid owns the child-count and condition constraints.
func (c ChildWaitSpec) required() uint32 {
	switch c.Condition.kind {
	case childWaitAll:
		return uint32(len(c.Children))
	case childWaitAny:
		return 1
	default:
		return c.Condition.quorum
	}
}

func (c ChildWaitSpec) wire() childWaitSpecWire {
	return childWaitSpecWire{
		Key: c.Key, Children: slices.Clone(c.Children),
		Boundary:  c.Boundary,
		Condition: childWaitConditionWire{Kind: c.Condition.kind, Quorum: c.Condition.quorum},
	}
}

func (c ChildWaitSpec) clone() ChildWaitSpec {
	cloned := c
	cloned.Children = slices.Clone(c.Children)
	return cloned
}

// NewChildWaitEffect creates a Framework Effect that opens an Engine-owned wait
// over direct children. Dispatch returns immediately with a WaitID; child work
// never blocks Execution.Step or holds a prepared Step open.
func NewChildWaitEffect(spec ChildWaitSpec) (Effect, error) {
	if !spec.Valid() {
		return Effect{}, ErrInvalidChildWait
	}
	return newFrameworkEffect(childWaitEffectWire{Operation: frameworkOperationWaitChildren, Spec: spec.wire()})
}

// ChildWaitOpened is the definite acknowledgement that the Engine opened a
// child wait and minted its WaitID. The declaring Effect owns the spec; the
// acknowledgement carries only the identity the Engine minted.
type ChildWaitOpened struct {
	waitID WaitID
}

func (c ChildWaitOpened) WaitID() WaitID { return c.waitID }

func (c ChildWaitOpened) Valid() bool { return c.waitID.Valid() }

// ParseChildWaitOpened decodes the settlement Signal produced by
// NewChildWaitEffect and verifies its Engine-owned Signal identity and attached WaitID.
func ParseChildWaitOpened(signal Signal) (ChildWaitOpened, error) {
	waitID, addressed := signal.WaitID()
	if !signal.EngineOwned() || !addressed {
		return ChildWaitOpened{}, ErrInvalidChildWait
	}
	wire, err := jsonwire.Decode[childWaitOpenedWire](signal.Payload())
	if err != nil {
		return ChildWaitOpened{}, fmt.Errorf("%w: decode opened Signal: %w", ErrInvalidChildWait, err)
	}
	if wire.Operation != childWaitSignalOpened {
		return ChildWaitOpened{}, ErrInvalidChildWait
	}
	return ChildWaitOpened{waitID: waitID}, nil
}

// UnresolvedEffect identifies an unsettled external effect at a drained subtree
// boundary. It is an observation; only the owning Process can settle the Effect.
type UnresolvedEffect struct {
	ProcessID ProcessID `json:"process_id"`
	EffectID  EffectID  `json:"effect_id"`
}

func (u UnresolvedEffect) Valid() bool { return u.ProcessID.Valid() && u.EffectID.Valid() }

func (u UnresolvedEffect) compare(other UnresolvedEffect) int {
	if order := cmp.Compare(u.ProcessID.String(), other.ProcessID.String()); order != 0 {
		return order
	}
	return cmp.Compare(u.EffectID.String(), other.EffectID.String())
}

// ChildOutcome is one child's immutable terminal Result and, when its wait
// drained the child's subtree, the Effects that subtree left unresolved. The
// wait that produced it owns the child's key and boundary.
type ChildOutcome struct {
	result Result
	// descendantUnresolvedEffects is present exactly at a drained boundary,
	// where an empty list proves the descendants resolved. The child's own
	// unresolved Effects belong to its Termination and are not repeated here.
	descendantUnresolvedEffects *[]UnresolvedEffect
}

func (c ChildOutcome) Result() Result { return c.result }

// SubtreeUnresolvedEffects returns an independent, ProcessID/EffectID-ordered
// projection including this child and all descendants. The boolean is true only
// for a drained subtree; false must not be interpreted as an empty subtree.
func (c ChildOutcome) SubtreeUnresolvedEffects() ([]UnresolvedEffect, bool) {
	if c.descendantUnresolvedEffects == nil {
		return nil, false
	}
	effects := slices.Clone(*c.descendantUnresolvedEffects)
	for _, effectID := range c.result.Termination().UnresolvedEffectIDs() {
		effects = append(effects, UnresolvedEffect{ProcessID: c.result.ProcessID(), EffectID: effectID})
	}
	slices.SortFunc(effects, UnresolvedEffect.compare)
	return effects, true
}

// SubtreeResolved reports whether this outcome proves that the child and all
// of its descendants drained without retained Unknown settlements. A
// terminal-result outcome proves nothing about the subtree and reports false.
func (c ChildOutcome) SubtreeResolved() bool {
	return c.descendantUnresolvedEffects != nil && len(*c.descendantUnresolvedEffects) == 0 &&
		len(c.result.Termination().UnresolvedEffectIDs()) == 0
}

// drained reports whether the outcome carries the evidence of a drained
// boundary.
func (c ChildOutcome) drained() bool { return c.descendantUnresolvedEffects != nil }

func (c ChildOutcome) Valid() bool {
	if !c.result.Valid() {
		return false
	}
	if c.descendantUnresolvedEffects == nil {
		return true
	}
	effects := *c.descendantUnresolvedEffects
	for index, effect := range effects {
		if !effect.Valid() || effect.ProcessID == c.result.ProcessID() || index > 0 && effects[index-1].compare(effect) >= 0 {
			return false
		}
	}
	return true
}

func (c ChildOutcome) wire() childOutcomeWire {
	wire := childOutcomeWire{Result: c.result.wire()}
	if c.descendantUnresolvedEffects != nil {
		wire.DescendantUnresolvedEffects = new(slices.Clone(*c.descendantUnresolvedEffects))
	}
	return wire
}

func (c ChildOutcome) MarshalJSON() ([]byte, error) {
	if !c.Valid() {
		return nil, ErrInvalidChildWait
	}
	return jsonv2.Marshal(c.wire())
}

func (c *ChildOutcome) UnmarshalJSON(data []byte) error {
	if c == nil {
		return ErrInvalidChildWait
	}
	wire, err := jsonwire.Decode[childOutcomeWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode child outcome: %w", ErrInvalidChildWait, err)
	}
	value, err := wire.value()
	if err != nil {
		return err
	}
	*c = value
	return nil
}

func (ChildOutcome) JSONSchemaAlias() any { return childOutcomeWire{} }

// ChildWaitSatisfied is one condition-satisfying, request-ordered child result
// set. For any or quorum it includes every child at the requested boundary at
// the atomic satisfaction check, without canceling or omitting based on status.
// The wait it answers owns the key and boundary; each outcome carries the
// evidence it observed, which Matches correlates with that boundary.
type ChildWaitSatisfied struct {
	waitID   WaitID
	outcomes []ChildOutcome
}

func (c ChildWaitSatisfied) WaitID() WaitID { return c.waitID }

// Outcomes returns terminal children in the original ChildWaitSpec order.
func (c ChildWaitSatisfied) Outcomes() []ChildOutcome {
	return slices.Clone(c.outcomes)
}

func (c ChildWaitSatisfied) Valid() bool {
	if !c.waitID.Valid() || len(c.outcomes) == 0 {
		return false
	}
	seen := make(map[ProcessID]struct{}, len(c.outcomes))
	for _, outcome := range c.outcomes {
		if !outcome.Valid() {
			return false
		}
		if _, duplicate := seen[outcome.result.ProcessID()]; duplicate {
			return false
		}
		seen[outcome.result.ProcessID()] = struct{}{}
	}
	return true
}

// Matches correlates a satisfaction with the entire active wait, including its
// boundary, required count, and request order.
func (c ChildWaitSatisfied) Matches(id WaitID, spec ChildWaitSpec) bool {
	if !c.Valid() || !spec.Valid() || c.waitID != id || uint32(len(c.outcomes)) < spec.required() {
		return false
	}
	drained := spec.Boundary == ChildWaitBoundaryDrained
	next := 0
	for _, outcome := range c.outcomes {
		if outcome.drained() != drained {
			return false
		}
		for next < len(spec.Children) && spec.Children[next] != outcome.Result().ProcessID() {
			next++
		}
		if next == len(spec.Children) {
			return false
		}
		next++
	}
	return true
}

// ParseChildWaitSatisfied decodes an Engine-generated, WaitID-addressed child
// wait-satisfaction Signal. Caller-owned Signal identities are rejected.
func ParseChildWaitSatisfied(signal Signal) (ChildWaitSatisfied, error) {
	waitID, addressed := signal.WaitID()
	if !signal.EngineOwned() || !addressed {
		return ChildWaitSatisfied{}, ErrInvalidChildWait
	}
	wire, err := jsonwire.Decode[childWaitSatisfiedWire](signal.Payload())
	if err != nil {
		return ChildWaitSatisfied{}, fmt.Errorf("%w: decode completion Signal: %w", ErrInvalidChildWait, err)
	}
	if wire.Operation != childWaitSignalSatisfied || len(wire.Outcomes) == 0 {
		return ChildWaitSatisfied{}, ErrInvalidChildWait
	}
	completed := ChildWaitSatisfied{waitID: waitID}
	for _, encoded := range wire.Outcomes {
		outcome, err := encoded.value()
		if err != nil {
			return ChildWaitSatisfied{}, err
		}
		completed.outcomes = append(completed.outcomes, outcome)
	}
	if !completed.Valid() {
		return ChildWaitSatisfied{}, ErrInvalidChildWait
	}
	return completed, nil
}

type childWaitSignalKind string

const (
	childWaitSignalOpened    childWaitSignalKind = "child_wait_opened"
	childWaitSignalSatisfied childWaitSignalKind = "child_wait_satisfied"
)

type childWaitConditionWire struct {
	Kind   childWaitKind `json:"kind"`
	Quorum uint32        `json:"quorum,omitzero"`
}

type childWaitSpecWire struct {
	Key       WaitKey                `json:"key"`
	Children  []ProcessID            `json:"children"`
	Boundary  ChildWaitBoundary      `json:"boundary"`
	Condition childWaitConditionWire `json:"condition"`
}

type childWaitEffectWire struct {
	Operation frameworkOperationKind `json:"operation"`
	Spec      childWaitSpecWire      `json:"spec"`
}

type childWaitOpenedWire struct {
	Operation childWaitSignalKind `json:"operation"`
}

type childWaitSatisfiedWire struct {
	Operation childWaitSignalKind `json:"operation"`
	Outcomes  []childOutcomeWire  `json:"outcomes"`
}

// childOutcomeWire keeps an explicitly empty descendant list: its presence is
// what distinguishes a drained subtree from a terminal result.
type childOutcomeWire struct {
	Result                      resultWire          `json:"result"`
	DescendantUnresolvedEffects *[]UnresolvedEffect `json:"descendant_unresolved_effects,omitzero"`
}

type resultWire struct {
	ProcessID   ProcessID   `json:"process_id"`
	StartedAt   time.Time   `json:"started_at"`
	FinishedAt  time.Time   `json:"finished_at"`
	Termination Termination `json:"termination"`
	Usage       Usage       `json:"usage"`
}

func (r *resultWire) UnmarshalJSON(data []byte) error {
	type wire resultWire
	value, err := jsonwire.Decode[wire](data, "usage")
	if err != nil {
		return err
	}
	*r = resultWire(value)
	return nil
}

func (c childWaitSpecWire) value() (ChildWaitSpec, error) {
	spec := ChildWaitSpec{
		Key: c.Key, Children: slices.Clone(c.Children),
		Boundary:  c.Boundary,
		Condition: ChildWaitCondition{kind: c.Condition.Kind, quorum: c.Condition.Quorum},
	}
	if !spec.Valid() {
		return ChildWaitSpec{}, ErrInvalidChildWait
	}
	return spec, nil
}

func decodeChildWaitEffect(payload json.RawMessage) (ChildWaitSpec, error) {
	wire, err := jsonwire.Decode[childWaitEffectWire](payload)
	if err != nil {
		return ChildWaitSpec{}, fmt.Errorf("%w: decode request: %w", ErrInvalidChildWait, err)
	}
	if wire.Operation != frameworkOperationWaitChildren {
		return ChildWaitSpec{}, ErrInvalidChildWait
	}
	return wire.Spec.value()
}

// childWaitOpenedPayload is the normalized payload of every opening Signal.
func childWaitOpenedPayload() json.RawMessage {
	return json.RawMessage(`{"operation":"` + string(childWaitSignalOpened) + `"}`)
}

func (c childOutcomeWire) value() (ChildOutcome, error) {
	result, err := c.Result.value()
	if err != nil {
		return ChildOutcome{}, err
	}
	outcome := ChildOutcome{result: result}
	if c.DescendantUnresolvedEffects != nil {
		outcome.descendantUnresolvedEffects = new(append([]UnresolvedEffect{}, *c.DescendantUnresolvedEffects...))
	}
	if !outcome.Valid() {
		return ChildOutcome{}, ErrInvalidChildWait
	}
	return outcome, nil
}

func (r resultWire) value() (Result, error) {
	result := Result{
		processID: r.ProcessID, startedAt: canonicalTime(r.StartedAt), finishedAt: canonicalTime(r.FinishedAt),
		termination: r.Termination, usage: r.Usage,
	}
	if !result.Valid() {
		return Result{}, ErrInvalidChildWait
	}
	return result, nil
}

func encodeChildWaitSatisfied(waitID WaitID, outcomes []ChildOutcome) (Signal, error) {
	completed := ChildWaitSatisfied{waitID: waitID, outcomes: slices.Clone(outcomes)}
	if !completed.Valid() {
		return Signal{}, ErrInvalidChildWait
	}
	wire := childWaitSatisfiedWire{Operation: childWaitSignalSatisfied, Outcomes: make([]childOutcomeWire, len(outcomes))}
	for index, outcome := range outcomes {
		wire.Outcomes[index] = outcome.wire()
	}
	payload, err := jsonv2.Marshal(wire)
	if err != nil {
		return Signal{}, err
	}
	return NewSignal(waitID.childWaitSignalID(), waitID, payload)
}
