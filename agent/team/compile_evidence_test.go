package team

import (
	"context"
	"errors"
	"testing"

	agentrun "github.com/Tangerg/scope/agent/run"
)

func TestTypedTeamCompilerPinsWorkerGrantsAndRefusesInvalidDefinitions(t *testing.T) {
	f := fixtureConfig(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "done"), nil }, echo())
	c := Config{Name: f.Name, Description: f.Description, Coordinator: f.Coordinator, Workers: f.Workers, MaxConcurrentTasks: f.MaxConcurrentTasks, MaxControlsPerTurn: f.MaxControlsPerTurn, BuildIdentity: agentrun.ComputeDigest([]byte("team-build"))}
	p, err := Compile[string, string](t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	rt := require(agentrun.New(t.Context(), agentrun.Config{Store: agentrun.NewMemoryStore()}))
	defer func() {
		if e := rt.Close(t.Context()); e != nil {
			t.Error(e)
		}
	}()
	if got, runErr := rt.Run(t.Context(), p, "begin"); got != "done" || runErr != nil {
		t.Fatal(got, runErr)
	}
	c.Workers[0].Budget.Steps = agentrun.NewQuota(128)
	revised, err := Compile[string, string](t.Context(), c)
	if err != nil || p.ProgramRef() == revised.ProgramRef() {
		t.Fatal("worker grant not bound", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if q, err := Compile[string, string](ctx, c); q.Valid() || !errors.Is(err, context.Canceled) {
		t.Fatal(q, err)
	}
	noBuild := c
	noBuild.BuildIdentity = agentrun.Digest{}
	if q, err := Compile[string, string](t.Context(), noBuild); q.Valid() || !errors.Is(err, agentrun.ErrInvalidProgram) {
		t.Fatal(q, err)
	}
	if q, err := Compile[chan int, string](t.Context(), c); q.Valid() || err == nil {
		t.Fatal("unrepresentable input accepted", err)
	}
	if q, err := Compile[string, chan int](t.Context(), c); q.Valid() || err == nil {
		t.Fatal("unrepresentable output accepted", err)
	}
	c.Coordinator = WorkerConfig{}
	if q, err := Compile[string, string](t.Context(), c); q.Valid() || err == nil {
		t.Fatal("missing coordinator accepted", err)
	}
}
