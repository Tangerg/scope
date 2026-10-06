package agent

import (
	"cmp"
	"errors"
)

var ErrInvalidProcessRelation = errors.New("agent: invalid process relation")

// ProcessRelation is the immutable location of one Process in an Engine-owned
// tree. Roots identify themselves as RootID at depth zero. Children have one
// parent and one stable ChildKey; a Process never has multiple parents.
type ProcessRelation struct {
	processID ProcessID
	parentID  ProcessID
	rootID    ProcessID
	childKey  ChildKey
	depth     uint32
}

func rootProcessRelation(id ProcessID) ProcessRelation {
	return ProcessRelation{processID: id, rootID: id}
}

func childProcessRelation(
	id ProcessID,
	parent ProcessRelation,
	key ChildKey,
) ProcessRelation {
	return ProcessRelation{
		processID: id,
		parentID:  parent.processID,
		rootID:    parent.rootID,
		childKey:  key,
		depth:     parent.depth + 1,
	}
}

func (p ProcessRelation) ProcessID() ProcessID { return p.processID }

// ParentID returns the direct parent and true for a child, or zero and false
// for a root.
func (p ProcessRelation) ParentID() (ProcessID, bool) {
	return p.parentID, p.parentID.Valid()
}

func (p ProcessRelation) RootID() ProcessID { return p.rootID }

// ChildKey returns the parent-scoped logical child identity and true for a
// child, or zero and false for a root.
func (p ProcessRelation) ChildKey() (ChildKey, bool) {
	return p.childKey, p.childKey.Valid()
}

// Depth returns zero for a root and parent depth plus one for every child.
func (p ProcessRelation) Depth() uint32 { return p.depth }

func (p ProcessRelation) IsRoot() bool {
	return p.Valid() && p.depth == 0
}

func (p ProcessRelation) Valid() bool {
	if !p.processID.Valid() || !p.rootID.Valid() {
		return false
	}
	if p.depth == 0 {
		return p.processID == p.rootID &&
			!p.parentID.Valid() && !p.childKey.Valid()
	}
	return p.parentID.Valid() && p.childKey.Valid() &&
		p.processID != p.parentID && p.processID != p.rootID
}

// childIdentity is the parent-scoped key that makes a child start idempotent.
type childIdentity struct {
	parent ProcessID
	key    ChildKey
}

// compareTreeOrder is the canonical tree order: parents precede descendants,
// and identities break ties so snapshot bytes and publication order are stable.
func (p ProcessRelation) compareTreeOrder(other ProcessRelation) int {
	if order := cmp.Compare(p.depth, other.depth); order != 0 {
		return order
	}
	return cmp.Compare(p.processID.String(), other.processID.String())
}

func (p ProcessRelation) childIdentity() (childIdentity, bool) {
	if !p.parentID.Valid() || !p.childKey.Valid() {
		return childIdentity{}, false
	}
	return childIdentity{parent: p.parentID, key: p.childKey}, true
}

// processRelationWire carries what a standalone record cannot derive: a root
// is its own root at depth zero, so only a child names its parent, key, root,
// and depth.
type processRelationWire struct {
	ParentID *ProcessID `json:"parent_id,omitzero"`
	RootID   *ProcessID `json:"root_id,omitzero"`
	ChildKey *ChildKey  `json:"child_key,omitzero"`
	Depth    uint32     `json:"depth,omitzero"`
}

func (p ProcessRelation) wire() processRelationWire {
	identity, child := p.childIdentity()
	if !child {
		return processRelationWire{}
	}
	return processRelationWire{
		ParentID: new(identity.parent), RootID: new(p.rootID),
		ChildKey: new(identity.key), Depth: p.depth,
	}
}

func processRelationFromWire(processID ProcessID, wire processRelationWire) (ProcessRelation, error) {
	if wire == (processRelationWire{}) {
		return rootProcessRelation(processID), nil
	}
	if wire.ParentID == nil || wire.RootID == nil || wire.ChildKey == nil {
		return ProcessRelation{}, ErrInvalidProcessRelation
	}
	relation := ProcessRelation{
		processID: processID, parentID: *wire.ParentID, rootID: *wire.RootID,
		childKey: *wire.ChildKey, depth: wire.Depth,
	}
	if !relation.Valid() {
		return ProcessRelation{}, ErrInvalidProcessRelation
	}
	return relation, nil
}
