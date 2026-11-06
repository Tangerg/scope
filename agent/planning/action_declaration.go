package planning

import "github.com/Tangerg/scope/agent/run"

// actionDeclaration carries only the facts needed to plan and declare Effects;
// executable capabilities never enter the definition's action manifest.
type actionDeclaration struct {
	action     Action
	policy     run.EffectPolicy
	child      run.ChildBinding
	childInput ChildInputFunc
}

func (a actionDeclaration) delegatesToChild() bool { return a.child.Valid() }

func (a actionDeclaration) valid() bool {
	if !a.action.Valid() {
		return false
	}
	if a.delegatesToChild() {
		return !a.policy.Valid()
	}
	return a.childInput == nil && a.policy.Valid()
}
