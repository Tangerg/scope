package goap

import (
	"container/heap"
	"context"
	"fmt"
	"math"
	"slices"

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
	problem       planning.Problem
	maxExpansions uint32
	startKey      string
	frontier      *frontier
	bestCosts     map[string]float64
	predecessors  map[string]predecessor
	nextOrder     uint64
	expansions    uint32
}

func newSearch(problem planning.Problem, maxExpansions uint32) *search {
	start := problem.InitialState()
	queue := &frontier{}
	heap.Init(queue)
	search := &search{
		problem: problem, maxExpansions: maxExpansions, startKey: start.Key(), frontier: queue,
		bestCosts: map[string]float64{start.Key(): 0}, predecessors: make(map[string]predecessor),
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
		if s.expansions == s.maxExpansions {
			return searchNode{}, false, fmt.Errorf("%w: %d", ErrExpansionLimitReached, s.maxExpansions)
		}
		s.expansions++
		if s.problem.Goal().SatisfiedBy(current.state) {
			return *current, true, nil
		}
		if err := s.expand(current); err != nil {
			return searchNode{}, false, err
		}
	}
	return searchNode{}, false, nil
}

func (s *search) expand(current *searchNode) error {
	currentKey := current.state.Key()
	for _, action := range s.problem.Actions() {
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
		cost := current.cost + edgeCost
		if math.IsInf(cost, 0) {
			return fmt.Errorf("%w: Action %q overflows cumulative cost", planning.ErrInvalidActionCost, action.Name())
		}
		if best, known := s.bestCosts[nextKey]; known && cost >= best {
			continue
		}
		planned, err := planning.NewPlannedAction(action.Name())
		if err != nil {
			return err
		}
		s.bestCosts[nextKey] = cost
		s.predecessors[nextKey] = predecessor{stateKey: currentKey, action: planned}
		s.push(nextState, cost)
	}
	return nil
}

func (s *search) reconstruct(goalKey string) ([]planning.PlannedAction, error) {
	var reversed []planning.PlannedAction
	for cursor := goalKey; cursor != s.startKey; {
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

func (s *search) hasGoalProducers() bool {
	initial := s.problem.InitialState()
	for _, required := range s.problem.Goal().Conditions() {
		if initial.Truth(required.Key()) == required.Truth() {
			continue
		}
		produced := false
		for _, action := range s.problem.Actions() {
			for _, effect := range action.Effects() {
				if effect.Key() == required.Key() && effect.Truth() == required.Truth() {
					produced = true
					break
				}
			}
			if produced {
				break
			}
		}
		if !produced {
			return false
		}
	}
	return true
}
