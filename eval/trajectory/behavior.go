package trajectory

import (
	"cmp"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"slices"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
)

type behaviorProjection struct {
	Termination behaviorTermination `json:"termination"`
	Output      json.RawMessage     `json:"output,omitzero"`
	Events      []behaviorEvent     `json:"events"`
	Models      []behaviorModel     `json:"models,omitempty"`
	Tools       []behaviorTool      `json:"tools,omitempty"`
}

type behaviorTermination struct {
	Status            agent.Status           `json:"status"`
	Cause             agent.TerminationCause `json:"cause"`
	FailureKind       agent.FailureKind      `json:"failure_kind,omitzero"`
	FailureCode       string                 `json:"failure_code,omitempty"`
	UnresolvedEffects int                    `json:"unresolved_effects,omitzero"`
}

type behaviorEventStream struct {
	processID agent.ProcessID
	phase     agent.EventPhase
}

type behaviorEvent struct {
	ProcessPath      string                 `json:"process_path"`
	Sequence         uint64                 `json:"sequence"`
	StepSequence     uint64                 `json:"step_sequence,omitzero"`
	Name             string                 `json:"name"`
	Phase            agent.EventPhase       `json:"phase"`
	ProcessStatus    agent.Status           `json:"process_status,omitzero"`
	TerminationCause agent.TerminationCause `json:"termination_cause,omitempty"`
	FailureKind      agent.FailureKind      `json:"failure_kind,omitzero"`
	FailureCode      string                 `json:"failure_code,omitempty"`
	StepStatus       agent.StepStatus       `json:"step_status,omitzero"`
	EffectTarget     agent.EffectTarget     `json:"effect_target,omitempty"`
	Settlement       agent.SettlementStatus `json:"settlement,omitzero"`
}

func (b behaviorEvent) compare(other behaviorEvent) int {
	if order := cmp.Compare(b.ProcessPath, other.ProcessPath); order != 0 {
		return order
	}
	if order := cmp.Compare(b.Phase, other.Phase); order != 0 {
		return order
	}
	return cmp.Compare(b.Sequence, other.Sequence)
}

type behaviorModel struct {
	ProcessPath string                 `json:"process_path"`
	Step        uint64                 `json:"step"`
	Sequence    uint64                 `json:"sequence"`
	Outcome     ModelOutcome           `json:"outcome"`
	ToolCalls   []behaviorToolDecision `json:"tool_calls,omitempty"`
}

type behaviorToolDecision struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitzero"`
}

func behaviorModelOf(call ModelCall, path string) (behaviorModel, error) {
	model := behaviorModel{ProcessPath: path, Step: call.StepSequence, Sequence: call.CallSequence, Outcome: call.Outcome()}
	for _, decision := range call.toolDecisions() {
		arguments, err := canonicalArguments(decision.Arguments)
		if err != nil {
			return behaviorModel{}, err
		}
		model.ToolCalls = append(model.ToolCalls, behaviorToolDecision{Name: decision.Name, Arguments: arguments})
	}
	return model, nil
}

type behaviorTool struct {
	ProcessPath string              `json:"process_path"`
	Step        uint64              `json:"step"`
	Name        string              `json:"name"`
	Arguments   json.RawMessage     `json:"arguments,omitzero"`
	Outcome     ToolOutcome         `json:"outcome"`
	Result      *behaviorToolResult `json:"result,omitzero"`
}

type behaviorToolResult struct {
	Name    string          `json:"name"`
	Output  chat.ToolOutput `json:"output"`
	IsError bool            `json:"is_error,omitzero"`
}

func behaviorToolOf(call ToolCall, path string) (behaviorTool, error) {
	arguments, err := canonicalArguments(call.Call.Arguments)
	if err != nil {
		return behaviorTool{}, err
	}
	tool := behaviorTool{
		ProcessPath: path, Step: call.StepSequence,
		Name:      call.Call.Name,
		Arguments: arguments, Outcome: call.Outcome(),
	}
	if call.Result != nil {
		result := call.Result.Clone()
		tool.Result = &behaviorToolResult{Name: result.Name, Output: result.Output, IsError: result.IsError}
	}
	return tool, nil
}

func behaviorTerminationOf(termination agent.Termination) behaviorTermination {
	projection := behaviorTermination{
		Status: termination.Status(), Cause: termination.Cause(),
		UnresolvedEffects: len(termination.UnresolvedEffectIDs()),
	}
	if failure, present := termination.Failure(); present {
		projection.FailureKind = failure.Kind()
		projection.FailureCode = failure.Code()
	}
	return projection
}

func (b *behaviorEvent) apply(event agent.Event) {
	if fact, present := event.ProcessFinished(); present {
		failure, failed := fact.Failure()
		b.ProcessStatus = fact.Status()
		b.TerminationCause = fact.Cause()
		if failed {
			b.FailureKind = failure.Kind()
			b.FailureCode = failure.Code()
		}
		return
	}
	if fact, present := event.StepFinished(); present {
		b.StepStatus = fact.Status()
		return
	}
	if fact, present := event.EffectStarted(); present {
		b.EffectTarget = fact.Target()
		return
	}
	if fact, present := event.EffectFinished(); present {
		b.EffectTarget = fact.Target()
		b.Settlement = fact.SettlementStatus()
		return
	}
	if fact, present := event.EffectResolved(); present {
		b.EffectTarget = fact.Target()
		b.Settlement = fact.SettlementStatus()
	}
}

func canonicalArguments(arguments string) (json.RawMessage, error) {
	if strings.TrimSpace(arguments) == "" {
		return nil, nil
	}
	value := jsontext.Value(arguments)
	if err := value.Format(jsontext.ReorderRawObjects(true)); err != nil {
		return nil, err
	}
	return json.RawMessage(value), nil
}

type behaviorChild struct {
	parent agent.ProcessID
	key    agent.ChildKey
}

// semanticProcessPaths substitutes only child identities that the Interaction owner
// can derive exactly from a recorded model decision. Custom Strategy ChildKeys
// remain semantic. Provider-generated ToolCall IDs are retained in the record
// but cannot perturb this projection's process identity.
func semanticProcessPaths(root agent.ProcessID, events []agent.Event, models []ModelCall) (map[agent.ProcessID]string, error) {
	relations, err := processRelationsOf(root, events)
	if err != nil {
		return nil, err
	}
	segments, err := interactionSegments(models)
	if err != nil {
		return nil, err
	}
	return relations.paths(root, func(parent agent.ProcessID, key agent.ChildKey) string {
		if segment, mapped := segments[behaviorChild{parent, key}]; mapped {
			return segment
		}
		return key.String()
	})
}

var interactionChildRoles = [...]struct {
	name string
	key  func(modelCall uint64, index uint32, call chat.ToolCall) (agent.ChildKey, error)
}{
	{"tool", func(modelCall uint64, index uint32, _ chat.ToolCall) (agent.ChildKey, error) {
		return interaction.ToolChildKey(modelCall, index)
	}},
	{"delegate", func(modelCall uint64, _ uint32, call chat.ToolCall) (agent.ChildKey, error) {
		return interaction.DelegateChildKey(modelCall, call)
	}},
}

func interactionSegments(models []ModelCall) (map[behaviorChild]string, error) {
	segments := make(map[behaviorChild]string)
	for _, model := range models {
		for index, decision := range model.toolDecisions() {
			for _, role := range interactionChildRoles {
				key, err := role.key(model.CallSequence, uint32(index), decision)
				if err != nil {
					return nil, err
				}
				identity := behaviorChild{model.ProcessID, key}
				segment := fmt.Sprintf("@interaction/%s/%020d/%010d", role.name, model.CallSequence, index)
				if previous, exists := segments[identity]; exists && previous != segment {
					return nil, fmt.Errorf("%w: ambiguous Interaction child attribution", ErrInvalidTrajectory)
				}
				segments[identity] = segment
			}
		}
	}
	return segments, nil
}

// semanticCallOrder is the one structural order used by deterministic behavior
// comparison and exact Tool assertions. It makes no claim about sibling timing.
type semanticCallOrder struct {
	paths  map[agent.ProcessID]string
	models []ModelCall
	tools  []ToolCall
}

func orderSemanticCalls(root agent.ProcessID, events []agent.Event, models []ModelCall, tools []ToolCall) (semanticCallOrder, error) {
	paths, err := semanticProcessPaths(root, events, models)
	if err != nil {
		return semanticCallOrder{}, err
	}
	attemptOrder := make(map[agent.EffectAttemptID]uint64)
	for _, event := range events {
		if fact, ok := event.EffectStarted(); ok {
			attemptOrder[fact.AttemptID()] = event.ProcessSequence()
		}
	}
	models = slices.Clone(models)
	slices.SortFunc(models, func(left, right ModelCall) int {
		if order := strings.Compare(paths[left.ProcessID], paths[right.ProcessID]); order != 0 {
			return order
		}
		return cmp.Compare(attemptOrder[left.AttemptID], attemptOrder[right.AttemptID])
	})
	tools = slices.Clone(tools)
	slices.SortFunc(tools, func(left, right ToolCall) int {
		if order := strings.Compare(paths[left.ProcessID], paths[right.ProcessID]); order != 0 {
			return order
		}
		return cmp.Compare(attemptOrder[left.AttemptID], attemptOrder[right.AttemptID])
	})
	return semanticCallOrder{paths: paths, models: models, tools: tools}, nil
}
