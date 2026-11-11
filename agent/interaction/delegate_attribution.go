package interaction

import (
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

type DelegateAttribution struct {
	reference         run.EffectID
	modelCallSequence uint64
	toolCallIndex     uint32
	toolCall          chat.ToolCall
	processID         run.ProcessID
}

func (d DelegateAttribution) ModelCallSequence() uint64       { return d.modelCallSequence }
func (d DelegateAttribution) ToolCallIndex() uint32           { return d.toolCallIndex }
func (d DelegateAttribution) ToolCall() chat.ToolCall         { return d.toolCall }
func (d DelegateAttribution) ChildKey() run.ChildKey          { key, _ := d.processID.Key(); return key }
func (d DelegateAttribution) ProcessID() run.ProcessID        { return d.processID }
func (d DelegateAttribution) Reference() (run.EffectID, bool) { return d.reference, d.Valid() }

func (d DelegateAttribution) Valid() bool {
	parent, child := d.processID.Parent()
	return child && parent == d.reference.Process && d.reference.Valid() && d.modelCallSequence > 0 && d.toolCall.Validate() == nil
}
