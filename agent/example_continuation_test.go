package agent_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/workflow"
)

type episodeState struct {
	Revision uint32 `json:"revision"`
	Summary  string `json:"summary"`
}

func episodeDeployment() (agent.Deployment, error) {
	stage, err := workflow.Transform("revise", func(_ context.Context, state episodeState) (episodeState, error) {
		if state.Revision >= 3 {
			return episodeState{}, errors.New("episode revision bound reached")
		}
		state.Revision++
		return state, nil
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: "example.episode", Description: "Advance one bounded revision.", Stages: []workflow.Stage{stage},
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	return episodeBinding(definition)
}

func episodeBinding(definition agent.Definition) (agent.Deployment, error) {
	return agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte("example-episode-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte(definition.Descriptor().Name() + "/revision-bound-3")),
	})
}

// The Host chooses an explicit new allocation; unused predecessor budget is
// never refunded. Request equality freezes state, behavior, limits, and authority.
type successorRequest struct {
	Predecessor   agent.ProcessID     `json:"predecessor"`
	DeploymentRef agent.DeploymentRef `json:"deployment_ref"`
	Input         agent.Input         `json:"input"`
	Limits        agent.Limits        `json:"limits"`
	TreeLimits    agent.TreeLimits    `json:"tree_limits"`
	Capabilities  agent.CapabilitySet `json:"capabilities"`
}

func (s successorRequest) identity() (agent.Digest, error) {
	if !s.Predecessor.Valid() || !s.DeploymentRef.Valid() || !s.Input.Valid() || !s.Limits.Valid() || !s.TreeLimits.Valid() || !s.Capabilities.Valid() {
		return agent.Digest{}, errors.New("invalid successor request")
	}
	encoded, err := agent.EncodeInput(s)
	if err != nil {
		return agent.Digest{}, err
	}
	return agent.ComputeDigest(encoded.JSON()), nil
}

// This example keeps continuation in the embedding Host. Its teaching store
// atomically links successor admission to the initial recoverable tree. A real
// database must perform those writes in one transaction. Engine.Start itself
// remains a fresh start; ambiguous creation is reconciled through that record.
func ExampleEngine_Start_successiveEpisodes() {
	ctx := context.Background()
	store := newEpisodeStore()
	deployment, err := episodeDeployment()
	if err != nil {
		panic(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store.trees})
	if err != nil {
		panic(err)
	}
	defer func() {
		if closeErr := engine.Close(ctx); closeErr != nil {
			panic(closeErr)
		}
	}()
	initial, err := agent.EncodeInput(episodeState{Summary: "reviewed plan"})
	if err != nil {
		panic(err)
	}
	previous, err := engine.Start(ctx, deployment, initial)
	if err != nil {
		panic(err)
	}
	result, err := store.sealEpisode(ctx, previous)
	if err != nil {
		panic(err)
	}
	output, present := result.Output()
	if !present {
		panic("completed episode has no output")
	}
	state, err := output.Decode[episodeState]()
	if err != nil {
		panic(err)
	}
	transfer, err := agent.EncodeInput(state)
	if err != nil {
		panic(err)
	}
	request := successorRequest{
		Predecessor: previous.ID(), DeploymentRef: deployment.DeploymentRef(), Input: transfer,
		Limits:     agent.Limits{MaxSteps: 8, MaxEffects: 4, MaxSignals: 8, MaxPendingSignals: 8},
		TreeLimits: agent.DefaultTreeLimits(),
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
	finalOutput, present := final.Output()
	if !present {
		panic("successor has no output")
	}
	nextState, err := finalOutput.Decode[episodeState]()
	if err != nil {
		panic(err)
	}
	fmt.Println("revision:", state.Revision, "->", nextState.Revision)
	fmt.Println("same successor:", next.ID() == duplicate.ID())
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
	engines []*agent.Engine
}

func (e *episodeHost) start(ctx context.Context, deployment agent.Deployment, request successorRequest) (*agent.Process, error) {
	if deployment.DeploymentRef() != request.DeploymentRef {
		return nil, agent.ErrInvalidDeployment
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

func (e *episodeHost) activate(ctx context.Context, deployment agent.Deployment, request successorRequest, attempt *episodeAttempt, existing agent.ProcessID) (*agent.Process, error) {
	engine, err := agent.NewEngine(agent.EngineConfig{
		TreeDurability: attempt, ProcessAdmitter: attempt,
		Limits: request.Limits, TreeLimits: request.TreeLimits, Capabilities: request.Capabilities,
	})
	if err != nil {
		return nil, err
	}
	e.engines = append(e.engines, engine)
	if !existing.Valid() {
		return engine.Start(ctx, deployment, request.Input)
	}
	head, present, err := e.store.trees.LoadTree(ctx, existing)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, errors.New("successor record has no committed tree")
	}
	return engine.RestoreTree(ctx, deployment, head)
}

func (e *episodeHost) close(ctx context.Context) error {
	var failures []error
	for _, engine := range e.engines {
		failures = append(failures, engine.Close(ctx))
	}
	return errors.Join(failures...)
}
