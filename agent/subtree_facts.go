package agent

import "slices"

// Both live and recovered trees query the same rule through read-only facts.
// The traversal never promotes working state to an acknowledged head.
func subtreeUnresolvedEffects(root ProcessID, children func(ProcessID) []ProcessID, termination func(ProcessID) Termination) []UnresolvedEffect {
	var effects []UnresolvedEffect
	var visit func(ProcessID)
	visit = func(id ProcessID) {
		for _, effectID := range termination(id).UnresolvedEffectIDs() {
			effects = append(effects, UnresolvedEffect{ProcessID: id, EffectID: effectID})
		}
		for _, child := range children(id) {
			visit(child)
		}
	}
	visit(root)
	slices.SortFunc(effects, UnresolvedEffect.compare)
	return effects
}
