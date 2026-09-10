package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type scopeJoinDefinition struct {
	descriptor Descriptor
	reference  DeploymentRef
	boundary   ChildWaitBoundary
}

func newScopeJoinDeployment(t *testing.T, boundary ChildWaitBoundary, dispatcher Dispatcher) Deployment {
	t.Helper()
	schema, err := SchemaFor[string]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name: "test.scope_join", Description: "Owns independently settling child scopes.", InputSchema: schema, OutputSchema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := &scopeJoinDefinition{descriptor: descriptor, boundary: boundary}
	deployment, err := NewDeployment(DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: ComputeDigest([]byte("scope-join")), ConfigurationDigest: ComputeDigest([]byte(boundary)),
	})
	if err != nil {
		t.Fatal(err)
	}
	definition.reference = deployment.DeploymentRef()
	return deployment
}

func (s *scopeJoinDefinition) Descriptor() Descriptor { return s.descriptor }

func (s *scopeJoinDefinition) Start(input Input) (Execution, error) {
	role, err := input.Decode[string]()
	if err != nil {
		return nil, err
	}
	return &scopeJoinExecution{definition: s, state: scopeJoinState{Role: role}}, nil
}

func (s *scopeJoinDefinition) Restore(state ExecutionState) (Execution, error) {
	if state.Kind() != s.descriptor.Name() {
		return nil, ErrInvalidExecutionState
	}
	value, err := wireJSON.decode[scopeJoinState](state.Payload())
	if err != nil {
		return nil, err
	}
	return &scopeJoinExecution{definition: s, state: value}, nil
}

type scopeJoinState struct {
	Role   string `json:"role"`
	Phase  string `json:"phase"`
	WaitID WaitID `json:"wait_id,omitzero"`
}

type scopeJoinExecution struct {
	definition *scopeJoinDefinition
	state      scopeJoinState
}

func (s *scopeJoinExecution) Step(_ context.Context, signals []Signal) (Transition, error) {
	if s.state.Phase == "" {
		s.state.Phase = "started"
		switch s.state.Role {
		case "root":
			return Continue(0, s.child("scope"), s.child("sibling"))
		case "scope":
			return Continue(0, s.child("cleanup"))
		default:
			payload, err := json.Marshal(struct {
				Name string `json:"name"`
			}{Name: s.state.Role})
			if err != nil {
				return Transition{}, err
			}
			effect, err := NewDispatcherEffect(payload)
			if err != nil {
				return Transition{}, err
			}
			return Continue(0, effect)
		}
	}
	consumed := uint32(len(signals))
	if s.state.Role == "scope" && s.state.Phase == "started" {
		s.state.Phase = "ready"
		return Pause(consumed, "wait for explicit scope completion")
	}
	if s.state.Role != "root" || s.state.Phase == "satisfied" {
		output, err := EncodeOutput(s.state.Role)
		if err != nil {
			return Transition{}, err
		}
		return Complete(consumed, output)
	}
	if s.state.Phase == "started" {
		var childID ProcessID
		for _, signal := range signals {
			result, err := ParseChildStartResult(signal)
			if err != nil {
				return Transition{}, err
			}
			if result.Key().String() == "scope" {
				childID, _ = result.ProcessID()
			}
		}
		key, _ := ParseWaitKey("scope-boundary")
		effect, err := WaitForChildren(ChildWaitSpec{
			Key: key, Children: []ProcessID{childID}, Boundary: s.definition.boundary, Condition: AllChildren(),
		})
		if err != nil {
			return Transition{}, err
		}
		s.state.Phase = "waiting"
		return Continue(consumed, effect)
	}
	for _, signal := range signals {
		if opened, err := ParseChildWaitOpened(signal); err == nil {
			s.state.WaitID = opened.WaitID()
			continue
		}
		satisfied, err := ParseChildWaitSatisfied(signal)
		if err != nil || satisfied.Boundary() != s.definition.boundary || len(satisfied.Outcomes()) != 1 {
			return Transition{}, errors.New("scope wait did not establish its requested boundary")
		}
		s.state.Phase = "satisfied"
		return Pause(consumed, "scope boundary reached; sibling remains active")
	}
	return Wait(consumed, s.state.WaitID)
}

func (s *scopeJoinExecution) child(role string) Effect {
	key, _ := ParseChildKey(role)
	input, _ := EncodeInput(role)
	budget := Budget{Steps: 20, Effects: 20, Signals: 40}
	if role == "scope" {
		budget = Budget{Steps: 40, Effects: 40, Signals: 80}
	}
	effect, err := StartChild(ChildSpec{
		Key: key, DeploymentRef: s.definition.reference, Input: input, Budget: budget,
	})
	if err != nil {
		panic(err)
	}
	return effect
}

func (s *scopeJoinExecution) Snapshot() (ExecutionState, error) {
	payload, err := json.Marshal(s.state)
	if err != nil {
		return ExecutionState{}, err
	}
	return NewExecutionState(s.definition.descriptor.Name(), payload)
}
