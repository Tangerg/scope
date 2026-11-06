package team

import (
	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
)

// obligationClaims is discarded after restoration; keeping it in execution
// state would give pending operations a second progress owner.
type obligationClaims struct {
	parent     run.ProcessID
	step       uint64
	effects    map[run.EffectID]struct{}
	batchStart *uint64
}

func (o *obligationClaims) claimParent(parent run.ProcessID) bool {
	if !parent.Valid() || o.parent.Valid() && o.parent != parent {
		return false
	}
	o.parent = parent
	return true
}

func (o *obligationClaims) claimEffect(effect run.EffectID) bool {
	if !effect.Valid() || !o.claimParent(effect.Process) || o.step != 0 && o.step != effect.Step {
		return false
	}
	if _, claimed := o.effects[effect]; claimed {
		return false
	}
	if o.effects == nil {
		o.effects = make(map[run.EffectID]struct{})
	}
	o.effects[effect] = struct{}{}
	o.step = effect.Step
	return true
}

func (o *obligationClaims) claimInvocation(invocation child.Invocation) bool {
	if invocation.Request != nil {
		return o.claimEffect(*invocation.Request)
	}
	if invocation.Process != nil {
		parent, _ := invocation.Process.Parent()
		return o.claimParent(parent)
	}
	return true
}

// A decision emits its appended tasks before its controls. Surviving receipts
// must identify the same task suffix even after earlier receipts have settled.
func (o *obligationClaims) claimBatchStart(start uint64) bool {
	if o.batchStart != nil {
		return *o.batchStart == start
	}
	o.batchStart = new(start)
	return true
}
