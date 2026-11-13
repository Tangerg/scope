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

// Valid reports whether d came from a HistoryProjection, which builds it only
// from an admitted delegated call and its committed child start.
func (d DelegateAttribution) Valid() bool { return d.reference.Valid() }
