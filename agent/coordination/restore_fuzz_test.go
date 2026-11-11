package coordination

import (
	"context"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
)

func FuzzFirstSuccessRestoreRoundTrip(f *testing.F) {
	compiler := conformancetest.Compiler(f)
	candidate := require(Compile[time.Time, time.Time](compiler, Config{Name: "test.candidate", Description: "Wait.", Policy: Deadline{}}))
	program := require(Compile[[]run.ChildSpec, FirstSuccessResult](compiler, Config{Name: "test.competition", Description: "Choose a candidate.", Policy: FirstSuccess{MaxCandidates: 2, Revision: "accept", Accept: func(context.Context, run.ChildKey, run.Termination) (bool, error) { return true, nil }}}))
	specs := []run.ChildSpec{
		{Key: require(run.ParseChildKey("a")), ProgramRef: candidate.ProgramRef(), Input: require(run.EncodePayload(time.Unix(0, 0).UTC()))},
		{Key: require(run.ParseChildKey("b")), ProgramRef: candidate.ProgramRef(), Input: require(run.EncodePayload(time.Unix(0, 0).UTC()))},
	}
	execution := require(program.Definition().Start(require(run.EncodePayload(specs))))
	initial := require(execution.Snapshot())
	require(execution.Step(f.Context(), turn(1)))
	requested := require(execution.Snapshot())
	process := require(turn(1).Process.Child(specs[0].Key))
	require(execution.Step(f.Context(), turn(2, run.Completion{Effect: turn(1).EffectID(0), Outcome: settled(process)})))
	started := require(execution.Snapshot())
	conformancetest.FuzzRestoreRoundTrip(f, program.Definition(), firstSuccessStateKind, initial, requested, started)
}
