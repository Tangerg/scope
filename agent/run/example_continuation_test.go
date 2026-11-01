package run_test

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
)

type episodeState struct {
	Revision uint32 `json:"revision"`
	Summary  string `json:"summary"`
}

const episodeRevisionLimit uint32 = 3

func episodeDeployment() (run.Program[run.Payload, run.Payload], error) {
	stage, err := workflow.Transform("revise", func(_ context.Context, state episodeState) (episodeState, error) {
		if state.Revision >= episodeRevisionLimit {
			return episodeState{}, errors.New("episode revision bound reached")
		}
		state.Revision++
		return state, nil
	})
	if err != nil {
		return run.Program[run.Payload, run.Payload]{}, err
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: "example.episode", Description: "Advance one bounded revision.", Stages: []workflow.Stage{stage},
	})
	if err != nil {
		return run.Program[run.Payload, run.Payload]{}, err
	}
	return episodeBinding(definition)
}

func episodeBinding(definition run.Definition) (run.Program[run.Payload, run.Payload], error) {
	return run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition:           definition,
		ImplementationDigest: run.ComputeDigest([]byte("example-episode-implementation")),
		ConfigurationDigest:  run.ComputeDigest(fmt.Appendf(nil, "%s/revision-bound-%d", definition.Descriptor().Name(), episodeRevisionLimit)),
	})
}

// The Host chooses an explicit new allocation; unused predecessor budget is
// never refunded. Request equality freezes state, behavior, limits, and authority.
type successorRequest struct {
	Predecessor  run.ProcessID     `json:"predecessor"`
	ProgramRef   run.ProgramRef    `json:"program_ref"`
	Input        run.Payload       `json:"input"`
	Budget       run.Budget        `json:"budget"`
	TreeLimits   run.TreeLimits    `json:"tree_limits"`
	Capabilities run.CapabilitySet `json:"capabilities"`
}

func (s successorRequest) identity() (run.Digest, error) {
	if !s.Predecessor.Valid() || !s.ProgramRef.Valid() || !s.Input.Valid() || !s.TreeLimits.Valid() {
		return run.Digest{}, errors.New("invalid successor request")
	}
	encoded, err := run.EncodePayload(s)
	if err != nil {
		return run.Digest{}, err
	}
	return run.ComputeDigest(encoded.JSON()), nil
}

// This example keeps continuation in the embedding Host. Its teaching store
// atomically links successor admission to the initial recoverable tree. A real
// database must perform those writes in one transaction. Runtime.Start itself
// remains a fresh start; ambiguous creation is reconciled through that record.
func ExampleRuntime_Start_successiveEpisodes() {
	ctx := context.Background()
	store := newEpisodeStore()
	deployment, err := episodeDeployment()
	if err != nil {
		panic(err)
	}
	engine, err := run.New(context.Background(), run.Config{Store: store.trees})
	if err != nil {
		panic(err)
	}
	defer func() {
		if closeErr := engine.Close(ctx); closeErr != nil {
			panic(closeErr)
		}
	}()
	initial, err := run.EncodePayload(episodeState{Summary: "reviewed plan"})
	if err != nil {
		panic(err)
	}
	previous, err := engine.Start(ctx, deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: initial})
	if err != nil {
		panic(err)
	}
	result, err := store.sealEpisode(ctx, previous)
	if err != nil {
		panic(err)
	}
	output, present := result.Termination().Output()
	if !present {
		panic("completed episode has no output")
	}
	state, err := output.Decode[episodeState]()
	if err != nil {
		panic(err)
	}
	transfer, err := run.EncodePayload(state)
	if err != nil {
		panic(err)
	}
	treeLimits := run.DefaultTreeLimits()
	treeLimits.MaxPendingSignals = 8
	request := successorRequest{
		Predecessor: previous.Relation().ProcessID(), ProgramRef: deployment.ProgramRef(), Input: transfer,
		Budget:     run.Budget{Steps: run.NewQuota(8), Effects: run.NewQuota(4), Signals: run.NewQuota(8)},
		TreeLimits: treeLimits,
	}
	host := &episodeHost{store: store}
	defer func() {
		if closeErr := host.close(ctx); closeErr != nil {
			panic(closeErr)
		}
	}()
	next, err := host.start(ctx, deployment, request)
	if err != nil {
		panic(err)
	}
	duplicate, err := host.start(ctx, deployment, request)
	if err != nil {
		panic(err)
	}
	final, err := next.Await(ctx)
	if err != nil {
		panic(err)
	}
	if joinErr := next.Join(ctx); joinErr != nil {
		panic(joinErr)
	}
	finalOutput, present := final.Termination().Output()
	if !present {
		panic("successor has no output")
	}
	nextState, err := finalOutput.Decode[episodeState]()
	if err != nil {
		panic(err)
	}
	fmt.Println("revision:", state.Revision, "->", nextState.Revision)
	fmt.Println("same successor:", next.Relation().ProcessID() == duplicate.Relation().ProcessID())
	fmt.Println("new allocations:", store.allocations)
	// Output:
	// revision: 1 -> 2
	// same successor: true
	// new allocations: 1
}

// Each Host owns its runtime instances. A replacement Host has no live handles
// and restores the already admitted tree; it does not create another successor.
type episodeHost struct {
	store   *episodeStore
	engines []*run.Runtime
}

func (e *episodeHost) start(ctx context.Context, deployment run.Program[run.Payload, run.Payload], request successorRequest) (*run.Handle[run.Payload], error) {
	if deployment.ProgramRef() != request.ProgramRef {
		return nil, run.ErrInvalidProgram
	}
	if err := deployment.Descriptor().ValidateInput(request.Input); err != nil {
		return nil, err
	}
	attempt, existing, err := e.store.claim(request)
	if err != nil {
		return nil, err
	}
	if existing.Valid() {
		for _, engine := range e.engines {
			if process, present := engine.Process(existing); present {
				return process, nil
			}
		}
	}
	return e.activate(ctx, deployment, request, attempt, existing)
}

func (e *episodeHost) activate(ctx context.Context, deployment run.Program[run.Payload, run.Payload], request successorRequest, attempt *episodeAttempt, existing run.ProcessID) (*run.Handle[run.Payload], error) {
	engine, err := run.New(ctx, run.Config{
		Store: attempt, ProcessAdmitter: attempt,
		Budget: request.Budget, TreeLimits: request.TreeLimits, Capabilities: request.Capabilities,
	})
	if err != nil {
		return nil, err
	}
	e.engines = append(e.engines, engine)
	if !existing.Valid() {
		return engine.Start(ctx, deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: request.Input})
	}
	head, present, err := e.store.trees.LoadTree(ctx, existing)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, errors.New("successor record has no committed tree")
	}
	return engine.Recover(ctx, deployment, head)
}

func (e *episodeHost) close(ctx context.Context) error {
	var failures []error
	for _, engine := range e.engines {
		failures = append(failures, engine.Close(ctx))
	}
	return errors.Join(failures...)
}
