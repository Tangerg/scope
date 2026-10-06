package agent

import "slices"

// Live and recovered trees share this rule through read-only accessors. The
// root's own unresolved Effects stay with its Termination, so only its
// descendants are listed.
func descendantUnresolvedEffects(root ProcessID, children func(ProcessID) []ProcessID, termination func(ProcessID) Termination) []UnresolvedEffect {
	// A drained subtree reports its list even when empty.
	effects := []UnresolvedEffect{}
	var visit func(ProcessID)
	visit = func(id ProcessID) {
		for _, child := range children(id) {
			for _, effectID := range termination(child).UnresolvedEffectIDs() {
				effects = append(effects, UnresolvedEffect{ProcessID: child, EffectID: effectID})
			}
			visit(child)
		}
	}
	visit(root)
	slices.SortFunc(effects, UnresolvedEffect.compare)
	return effects
}
