package agent

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
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

// AllChildren waits until every named child reaches the requested boundary.
func AllChildren() ChildWaitCondition { return ChildWaitCondition{kind: childWaitAll} }

// AnyChild waits until at least one named child reaches the requested boundary.
func AnyChild() ChildWaitCondition { return ChildWaitCondition{kind: childWaitAny} }

// ChildQuorum waits until count named children reach the requested boundary.
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

func (c ChildWaitCondition) required(total int) (uint32, error) {
	if !c.Valid() || total <= 0 || uint64(total) > uint64(^uint32(0)) {
		return 0, ErrInvalidChildWait
	}
	switch c.kind {
	case childWaitAll:
		return uint32(total), nil
	case childWaitAny:
		return 1, nil
	case childWaitQuorum:
		if c.quorum > uint32(total) {
			return 0, ErrInvalidChildWait
		}
		return c.quorum, nil
	default:
		return 0, ErrInvalidChildWait
	}
}

// ChildWaitBoundary selects the lifecycle fact counted by a child wait.
type ChildWaitBoundary string

const (
	ChildWaitBoundaryResult  ChildWaitBoundary = "terminal_result"
	ChildWaitBoundaryDrained ChildWaitBoundary = "subtree_drained"
)

func (c ChildWaitBoundary) Valid() bool {
	return c == ChildWaitBoundaryResult || c == ChildWaitBoundaryDrained
}

func (c ChildWaitBoundary) String() string {
	if !c.Valid() {
		return invalidEnumName
	}
	return string(c)
}

// ChildWaitSpec names one stable logical wait, its direct children in result
// order, lifecycle boundary, and count predicate.
type ChildWaitSpec struct {
	// Key is the Execution-owned logical identity of this wait request.
	Key WaitKey
	// Children lists direct child identities in result order.
	Children []ProcessID
	// Boundary explicitly selects terminal results or joined subtrees. A joined
	// subtree has completed its owned local work and required acknowledgments in
	// this runtime, as defined by Process.Join; remote uncertainty may remain.
	Boundary ChildWaitBoundary
	// Condition declares how many listed children must reach Boundary.
	Condition ChildWaitCondition
}

func (c ChildWaitSpec) Valid() bool {
	if !c.Key.Valid() || !c.Boundary.Valid() {
		return false
	}
	if _, err := c.Condition.required(len(c.Children)); err != nil {
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

// WaitForChildren creates a Framework Effect that opens an Engine-owned wait
// over direct children. Dispatch returns immediately with a WaitID; child work
// never blocks Execution.Step or holds a prepared Step open.
func WaitForChildren(spec ChildWaitSpec) (Effect, error) {
	if !spec.Valid() {
		return Effect{}, ErrInvalidChildWait
	}
	payload, err := json.Marshal(childWaitEffectWire{
		Operation: frameworkEffectWaitChildren,
		Spec:      spec.wire(),
	})
	if err != nil {
		return Effect{}, fmt.Errorf("%w: encode request: %w", ErrInvalidChildWait, err)
	}
	return newEffect(EffectTargetFramework, payload)
}

// ChildWaitOpened is the definite acknowledgement that the Engine registered
// a child wait and minted its WaitID.
type ChildWaitOpened struct {
	waitID WaitID
	spec   ChildWaitSpec
}

// WaitID returns the Engine-minted wait identity to store in Execution state.
func (c ChildWaitOpened) WaitID() WaitID { return c.waitID }

// Spec returns the immutable child-wait request acknowledged by Engine.
func (c ChildWaitOpened) Spec() ChildWaitSpec {
	spec := c.spec
	spec.Children = slices.Clone(c.spec.Children)
	return spec
}

func (c ChildWaitOpened) Valid() bool { return c.waitID.Valid() && c.spec.Valid() }

// ParseChildWaitOpened decodes the settlement Signal produced by
// WaitForChildren and verifies its Engine-owned Signal identity and attached WaitID.
func ParseChildWaitOpened(signal Signal) (ChildWaitOpened, error) {
	waitID, addressed := signal.WaitID()
	if !signal.EngineOwned() || !addressed {
		return ChildWaitOpened{}, ErrInvalidChildWait
	}
	wire, err := decodeJSON[childWaitOpenedWire](signal.Payload())
	if err != nil {
		return ChildWaitOpened{}, fmt.Errorf("%w: decode opened Signal: %w", ErrInvalidChildWait, err)
	}
	spec, err := wire.Spec.value()
	if err != nil || wire.Operation != childSignalWaitOpened {
		return ChildWaitOpened{}, ErrInvalidChildWait
	}
	opened := ChildWaitOpened{waitID: waitID, spec: spec}
	if !opened.Valid() {
		return ChildWaitOpened{}, ErrInvalidChildWait
	}
	return opened, nil
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

// ChildOutcome pairs a parent's logical ChildKey with the child's immutable
// terminal Result and the subtree facts established by the wait boundary.
type ChildOutcome struct {
	key                      ChildKey
	result                   Result
	subtreeUnresolvedEffects []UnresolvedEffect
}

// Key returns the parent-scoped logical child identity.
func (c ChildOutcome) Key() ChildKey { return c.key }

// Result returns the child's immutable terminal result.
func (c ChildOutcome) Result() Result { return c.result }

// SubtreeUnresolvedEffects returns an independent, ProcessID/EffectID-ordered
// projection including this child and all descendants. The boolean is true only
// for a Drained boundary; false must not be interpreted as an empty subtree.
func (c ChildOutcome) SubtreeUnresolvedEffects() ([]UnresolvedEffect, bool) {
	return slices.Clone(c.subtreeUnresolvedEffects), c.subtreeUnresolvedEffects != nil
}

func (c ChildOutcome) Valid() bool {
	if !c.key.Valid() || !c.result.Valid() {
		return false
	}
	var own []EffectID
	for index, effect := range c.subtreeUnresolvedEffects {
		if !effect.Valid() || index > 0 && c.subtreeUnresolvedEffects[index-1].compare(effect) >= 0 {
			return false
		}
		if effect.ProcessID == c.result.ProcessID() {
			own = append(own, effect.EffectID)
		}
	}
	if c.subtreeUnresolvedEffects != nil {
		expected := c.result.Termination().UnresolvedEffectIDs()
		if len(own) != len(expected) {
			return false
		}
		for _, id := range own {
			if !slices.Contains(expected, id) {
				return false
			}
		}
	}
	return true
}

func (c ChildOutcome) wire() childOutcomeWire {
	wire := childOutcomeWire{Key: c.key, Result: c.result.wire()}
	if c.subtreeUnresolvedEffects != nil {
		wire.SubtreeUnresolvedEffects = new(slices.Clone(c.subtreeUnresolvedEffects))
	}
	return wire
}

func (c ChildOutcome) MarshalJSON() ([]byte, error) {
	if !c.Valid() {
		return nil, ErrInvalidChildWait
	}
	return json.Marshal(c.wire())
}

func (c *ChildOutcome) UnmarshalJSON(data []byte) error {
	if c == nil {
		return ErrInvalidChildWait
	}
	wire, err := decodeJSON[childOutcomeWire](data)
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
type ChildWaitSatisfied struct {
	waitID   WaitID
	key      WaitKey
	boundary ChildWaitBoundary
	outcomes []ChildOutcome
}

// WaitID returns the addressed wait identity.
func (c ChildWaitSatisfied) WaitID() WaitID { return c.waitID }

// Key returns the logical wait key declared by the Execution.
func (c ChildWaitSatisfied) Key() WaitKey { return c.key }

// Boundary identifies the lifecycle fact established by this wait.
func (c ChildWaitSatisfied) Boundary() ChildWaitBoundary { return c.boundary }

// Outcomes returns terminal children in the original ChildWaitSpec order.
func (c ChildWaitSatisfied) Outcomes() []ChildOutcome {
	return slices.Clone(c.outcomes)
}

func (c ChildWaitSatisfied) Valid() bool {
	if !c.waitID.Valid() || !c.key.Valid() || !c.boundary.Valid() || len(c.outcomes) == 0 {
		return false
	}
	seen := make(map[ProcessID]struct{}, len(c.outcomes))
	for _, outcome := range c.outcomes {
		if !outcome.Valid() || (c.boundary == ChildWaitBoundaryDrained) != (outcome.subtreeUnresolvedEffects != nil) {
			return false
		}
		if _, duplicate := seen[outcome.result.ProcessID()]; duplicate {
			return false
		}
		seen[outcome.result.ProcessID()] = struct{}{}
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
	wire, err := decodeJSON[childWaitSatisfiedWire](signal.Payload())
	if err != nil {
		return ChildWaitSatisfied{}, fmt.Errorf("%w: decode completion Signal: %w", ErrInvalidChildWait, err)
	}
	if wire.Operation != childSignalWaitSatisfied || !wire.Key.Valid() || len(wire.Outcomes) == 0 {
		return ChildWaitSatisfied{}, ErrInvalidChildWait
	}
	completed := ChildWaitSatisfied{waitID: waitID, key: wire.Key, boundary: wire.Boundary}
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

type childSignalOperation string

const (
	childSignalWaitOpened    childSignalOperation = "child_wait_opened"
	childSignalWaitSatisfied childSignalOperation = "child_wait_satisfied"
)

type childWaitConditionWire struct {
	Kind   childWaitKind `json:"kind"`
	Quorum uint32        `json:"quorum,omitempty"`
}

type childWaitSpecWire struct {
	Key       WaitKey                `json:"key"`
	Children  []ProcessID            `json:"children"`
	Boundary  ChildWaitBoundary      `json:"boundary"`
	Condition childWaitConditionWire `json:"condition"`
}

type childWaitEffectWire struct {
	Operation frameworkEffectOperation `json:"operation"`
	Spec      childWaitSpecWire        `json:"spec"`
}

type childWaitOpenedWire struct {
	Operation childSignalOperation `json:"operation"`
	Spec      childWaitSpecWire    `json:"spec"`
}

type childWaitSatisfiedWire struct {
	Operation childSignalOperation `json:"operation"`
	Key       WaitKey              `json:"key"`
	Boundary  ChildWaitBoundary    `json:"boundary"`
	Outcomes  []childOutcomeWire   `json:"outcomes"`
}

type childOutcomeWire struct {
	Key                      ChildKey            `json:"key"`
	Result                   resultWire          `json:"result"`
	SubtreeUnresolvedEffects *[]UnresolvedEffect `json:"subtree_unresolved_effects,omitempty"`
}

type resultWire struct {
	ProcessID   ProcessID   `json:"process_id"`
	StartedAt   time.Time   `json:"started_at"`
	FinishedAt  time.Time   `json:"finished_at"`
	Output      *Output     `json:"output,omitempty"`
	Termination Termination `json:"termination"`
	Usage       Usage       `json:"usage"`
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
	wire, err := decodeJSON[childWaitEffectWire](payload)
	if err != nil {
		return ChildWaitSpec{}, fmt.Errorf("%w: decode request: %w", ErrInvalidChildWait, err)
	}
	if wire.Operation != frameworkEffectWaitChildren {
		return ChildWaitSpec{}, ErrInvalidChildWait
	}
	return wire.Spec.value()
}

func encodeChildWaitOpened(spec ChildWaitSpec) (json.RawMessage, error) {
	if !spec.Valid() {
		return nil, ErrInvalidChildWait
	}
	return json.Marshal(childWaitOpenedWire{
		Operation: childSignalWaitOpened,
		Spec:      spec.wire(),
	})
}

func (c childOutcomeWire) value() (ChildOutcome, error) {
	result, err := c.Result.value()
	if err != nil {
		return ChildOutcome{}, err
	}
	outcome := ChildOutcome{key: c.Key, result: result}
	if c.SubtreeUnresolvedEffects != nil {
		outcome.subtreeUnresolvedEffects = slices.Clone(*c.SubtreeUnresolvedEffects)
	}
	if !outcome.Valid() {
		return ChildOutcome{}, ErrInvalidChildWait
	}
	return outcome, nil
}

func (r resultWire) value() (Result, error) {
	var output Output
	if r.Output != nil {
		output = *r.Output
	}
	result := Result{
		processID: r.ProcessID, startedAt: r.StartedAt, finishedAt: r.FinishedAt,
		output: output, termination: r.Termination, usage: r.Usage,
	}
	if !result.Valid() {
		return Result{}, ErrInvalidChildWait
	}
	return result, nil
}

func encodeChildWaitSatisfied(
	waitID WaitID,
	key WaitKey,
	boundary ChildWaitBoundary,
	outcomes []ChildOutcome,
) (Signal, error) {
	completed := ChildWaitSatisfied{waitID: waitID, key: key, boundary: boundary, outcomes: slices.Clone(outcomes)}
	if !completed.Valid() {
		return Signal{}, ErrInvalidChildWait
	}
	wire := childWaitSatisfiedWire{
		Operation: childSignalWaitSatisfied,
		Key:       key,
		Boundary:  boundary,
		Outcomes:  make([]childOutcomeWire, len(outcomes)),
	}
	for index, outcome := range outcomes {
		wire.Outcomes[index] = outcome.wire()
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		return Signal{}, err
	}
	return newSignal(waitID.childWaitSignalID(), waitID, payload)
}
