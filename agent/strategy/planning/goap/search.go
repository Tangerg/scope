package goap

import (
	"container/heap"
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/planning"
)

type searchNode struct {
	state planning.WorldState
	cost  float64
	order uint64
}

type frontier []*searchNode

func (f frontier) Len() int { return len(f) }

func (f frontier) Less(left, right int) bool {
	if f[left].cost != f[right].cost {
		return f[left].cost < f[right].cost
	}
	return f[left].order < f[right].order
}

func (f frontier) Swap(left, right int) {
	f[left], f[right] = f[right], f[left]
}

func (f *frontier) Push(value any) { *f = append(*f, value.(*searchNode)) }

func (f *frontier) Pop() any {
	old := *f
	last := len(old) - 1
	value := old[last]
	old[last] = nil
	*f = old[:last]
	return value
}

type predecessor struct {
	stateKey string
	action   planning.PlannedAction
}

type search struct {
	problem           planning.Problem
	actions           []planning.Action
	maxExpansions     agent.Quota
	maxGeneratedNodes agent.Quota
	startKey          string
	frontier          *frontier
	bestCosts         map[string]float64
	predecessors      map[string]predecessor
	nextOrder         uint64
	expansions        uint64
}

func newSearch(problem planning.Problem, maxExpansions, maxGeneratedNodes agent.Quota) *search {
	start := problem.InitialState()
	startKey := start.Key()
	queue := &frontier{}
	heap.Init(queue)
	search := &search{
		problem: problem, actions: problem.Actions(), maxExpansions: maxExpansions, startKey: startKey, frontier: queue,
		bestCosts: map[string]float64{startKey: 0}, predecessors: make(map[string]predecessor),
		maxGeneratedNodes: maxGeneratedNodes,
	}
	search.push(start, 0)
	return search
}

func (s *search) push(state planning.WorldState, cost float64) {
	heap.Push(s.frontier, &searchNode{state: state, cost: cost, order: s.nextOrder})
	s.nextOrder++
}

func (s *search) run(ctx context.Context) (searchNode, bool, error) {
	for s.frontier.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return searchNode{}, false, err
		}
		current := heap.Pop(s.frontier).(*searchNode)
		currentKey := current.state.Key()
		if current.cost != s.bestCosts[currentKey] {
			continue
		}
		if !s.maxExpansions.Allows(s.expansions, 1) {
			return searchNode{}, false, ErrExpansionLimitReached
		}
		if s.expansions == ^uint64(0) {
			return searchNode{}, false, agent.ErrCounterExhausted
		}
		s.expansions++
		if s.problem.Goal().SatisfiedBy(current.state) {
			return *current, true, nil
		}
		if err := s.expand(ctx, current, currentKey); err != nil {
			return searchNode{}, false, err
		}
	}
	return searchNode{}, false, nil
}

func (s *search) expand(ctx context.Context, current *searchNode, currentKey string) error {
	for _, action := range s.actions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !action.Applicable(current.state) {
			continue
		}
		nextState, err := action.Apply(current.state)
		if err != nil {
			return fmt.Errorf("goap: apply Action %q at state %q: %w", action.Name(), currentKey, err)
		}
		nextKey := nextState.Key()
		if nextKey == currentKey {
			continue
		}
		edgeCost, err := action.Cost(current.state)
		if err != nil {
			return fmt.Errorf("goap: Action %q at state %q: %w", action.Name(), currentKey, err)
		}
		if cancelErr := ctx.Err(); cancelErr != nil {
			return cancelErr
		}
		if err := s.relax(action, currentKey, nextState, nextKey, current.cost+edgeCost); err != nil {
			return err
		}
	}
	return nil
}

func (s *search) relax(action planning.Action, fromKey string, state planning.WorldState, key string, cost float64) error {
	if math.IsInf(cost, 0) {
		return fmt.Errorf("%w: Action %q overflows cumulative cost", planning.ErrInvalidActionCost, action.Name())
	}
	if best, known := s.bestCosts[key]; known && cost >= best {
		return nil
	}
	if !s.maxGeneratedNodes.Allows(s.nextOrder, 1) {
		return ErrGenerationLimitReached
	}
	if s.nextOrder == ^uint64(0) {
		return agent.ErrCounterExhausted
	}
	planned, err := planning.NewPlannedAction(action.Name())
	if err != nil {
		return err
	}
	s.bestCosts[key] = cost
	s.predecessors[key] = predecessor{stateKey: fromKey, action: planned}
	s.push(state, cost)
	return nil
}

func (s *search) plan(ctx context.Context, goal searchNode) (planning.Plan, error) {
	actions, err := s.reconstruct(ctx, goal.state.Key())
	if err != nil {
		return planning.Plan{}, err
	}
	return planning.NewPlan(actions, goal.cost)
}

func (s *search) reconstruct(ctx context.Context, goalKey string) ([]planning.PlannedAction, error) {
	var reversed []planning.PlannedAction
	for cursor := goalKey; cursor != s.startKey; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		previous, found := s.predecessors[cursor]
		if !found {
			return nil, fmt.Errorf("goap: predecessor missing for state %q", cursor)
		}
		reversed = append(reversed, previous.action)
		cursor = previous.stateKey
	}
	slices.Reverse(reversed)
	return reversed, nil
}

func (s *search) hasGoalProducers(ctx context.Context) (bool, error) {
	initial := s.problem.InitialState()
	for _, required := range s.problem.Goal().Conditions() {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if initial.Truth(required.Key()) == required.Truth() {
			continue
		}
		produced, err := s.produces(ctx, required)
		if err != nil || !produced {
			return false, err
		}
	}
	return true, ctx.Err()
}

func (s *search) produces(ctx context.Context, required planning.Condition) (bool, error) {
	for _, action := range s.actions {
		for _, effect := range action.Effects() {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			if effect == required {
				return true, nil
			}
		}
	}
	return false, nil
}
