package planning

import (
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
)

func FuzzPlanningRestoreRoundTrip(f *testing.F) {
	config := validationConfig(f)
	definition := planningValue(config.definition())
	input := planningValue(run.EncodePayload("task"))
	seeds := []executionState{
		{Input: input, WorldState: validationWorld(f), Sense: &awaitedSense{Effect: validationTurn(1).EffectID(0)}},
		{Input: input, WorldState: validationWorld(f), Action: &awaitedAction{Name: config.actions[0].action.Name(), Effect: validationTurn(2).EffectID(0)}},
	}
	states := make([]run.ExecutionState, len(seeds))
	for index, seed := range seeds {
		states[index] = planningValue(seed.snapshot())
		if _, err := definition.Restore(f.Context(), states[index]); err != nil {
			f.Fatalf("seed %d is not a restorable state: %v", index, err)
		}
	}
	conformancetest.FuzzRestoreRoundTrip(f, definition, executionStateKind, states...)
}
