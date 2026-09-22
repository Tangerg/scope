package agent

import "errors"

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

// ProcessID returns the Process located by this relation.
func (p ProcessRelation) ProcessID() ProcessID { return p.processID }

// ParentID returns the direct parent and true for a child, or zero and false
// for a root.
func (p ProcessRelation) ParentID() (ProcessID, bool) {
	return p.parentID, p.parentID.Valid()
}

// RootID returns the stable root identity shared by the complete Process tree.
func (p ProcessRelation) RootID() ProcessID { return p.rootID }

// ChildKey returns the parent-scoped logical child identity and true for a
// child, or zero and false for a root.
func (p ProcessRelation) ChildKey() (ChildKey, bool) {
	return p.childKey, p.childKey.Valid()
}

// Depth returns zero for a root and parent depth plus one for every child.
func (p ProcessRelation) Depth() uint32 { return p.depth }

// IsRoot reports whether p identifies the root of its tree.
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

type processRelationWire struct {
	ParentID *ProcessID `json:"parent_id,omitzero"`
	RootID   ProcessID  `json:"root_id"`
	ChildKey *ChildKey  `json:"child_key,omitzero"`
	Depth    uint32     `json:"depth"`
}

func (p ProcessRelation) wire() processRelationWire {
	wire := processRelationWire{RootID: p.rootID, Depth: p.depth}
	if p.parentID.Valid() {
		parentID := p.parentID
		wire.ParentID = &parentID
	}
	if p.childKey.Valid() {
		childKey := p.childKey
		wire.ChildKey = &childKey
	}
	return wire
}

func processRelationFromWire(processID ProcessID, wire processRelationWire) (ProcessRelation, error) {
	relation := ProcessRelation{processID: processID, rootID: wire.RootID, depth: wire.Depth}
	if wire.ParentID != nil {
		relation.parentID = *wire.ParentID
	}
	if wire.ChildKey != nil {
		relation.childKey = *wire.ChildKey
	}
	if !relation.Valid() {
		return ProcessRelation{}, ErrInvalidProcessRelation
	}
	return relation, nil
}
