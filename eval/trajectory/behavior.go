package trajectory

import (
	"cmp"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/run"
	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval"
)

const rootProcessPath = "root"

type behaviorProjection struct {
	Termination behaviorTermination `json:"termination"`
	Output      json.RawMessage     `json:"output,omitzero"`
	Processes   []behaviorProcess   `json:"processes"`
	Steps       []behaviorStep      `json:"steps"`
	Effects     []behaviorEffect    `json:"effects"`
	Controls    []behaviorControl   `json:"controls"`
	Models      []behaviorModel     `json:"models"`
	Tools       []behaviorTool      `json:"tools"`
}

type behaviorTermination struct {
	Status      run.Status           `json:"status"`
	Cause       run.TerminationCause `json:"cause"`
	FailureKind run.FailureKind      `json:"failure_kind,omitzero"`
	FailureCode string               `json:"failure_code,omitzero"`
}

func behaviorTerminationOf(termination run.Termination) behaviorTermination {
	value := behaviorTermination{Status: termination.Status(), Cause: termination.Cause()}
	if failure, present := termination.Failure(); present {
		value.FailureKind, value.FailureCode = failure.Kind(), failure.Code()
	}
	return value
}

type behaviorProcess struct {
	Path        string              `json:"path"`
	Program     string              `json:"program"`
	Termination behaviorTermination `json:"termination"`
}

type behaviorStep struct {
	Process string             `json:"process"`
	Step    uint64             `json:"step"`
	Kind    run.TransitionKind `json:"kind"`
}

type behaviorEffect struct {
	Process string               `json:"process"`
	Step    uint64               `json:"step"`
	Index   uint32               `json:"index"`
	Target  run.EffectTarget     `json:"target"`
	Status  run.SettlementStatus `json:"status"`
}

type behaviorControl struct {
	Process  string          `json:"process"`
	Sequence uint64          `json:"sequence"`
	Kind     run.ControlKind `json:"kind"`
}

type behaviorModel struct {
	Process   string                   `json:"process"`
	Step      uint64                   `json:"step"`
	Index     uint32                   `json:"index"`
	Outcome   interaction.ModelOutcome `json:"outcome"`
	ToolCalls []behaviorToolDecision   `json:"tool_calls"`
}

type behaviorToolDecision struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitzero"`
}

func behaviorModelOf(call ModelCall, path string) (behaviorModel, error) {
	value := behaviorModel{Process: path, Step: call.EffectID.Step, Index: call.EffectID.Index, Outcome: call.Settlement.Outcome}
	for _, decision := range call.toolDecisions() {
		arguments, err := canonicalArguments(decision.Arguments)
		if err != nil {
			return behaviorModel{}, fmt.Errorf("%w: model tool arguments: %w", ErrInvalidTrajectory, err)
		}
		value.ToolCalls = append(value.ToolCalls, behaviorToolDecision{Name: decision.Name, Arguments: arguments})
	}
	return value, nil
}

type behaviorTool struct {
	Process        string                  `json:"process"`
	Step           uint64                  `json:"step"`
	Index          uint32                  `json:"index"`
	ContinuationOf *behaviorEffectPosition `json:"continuation_of,omitzero"`
	Name           string                  `json:"name"`
	Arguments      json.RawMessage         `json:"arguments,omitzero"`
	Outcome        interaction.ToolOutcome `json:"outcome,omitzero"`
	Result         *behaviorToolResult     `json:"result,omitzero"`
}

type behaviorEffectPosition struct {
	Step  uint64 `json:"step"`
	Index uint32 `json:"index"`
}

type behaviorToolResult struct {
	Output  toolcontract.Output `json:"output"`
	IsError bool                `json:"is_error,omitzero"`
}

func behaviorToolOf(call ToolCall, path string) (behaviorTool, error) {
	arguments, err := canonicalArguments(call.Call.Arguments)
	if err != nil {
		return behaviorTool{}, fmt.Errorf("%w: tool arguments: %w", ErrInvalidTrajectory, err)
	}
	value := behaviorTool{Process: path, Step: call.EffectID.Step, Index: call.EffectID.Index, Name: call.Call.Name, Arguments: arguments, Outcome: call.Outcome()}
	if call.ContinuationOf.Valid() {
		if call.ContinuationOf.Process != call.EffectID.Process {
			return behaviorTool{}, fmt.Errorf("%w: tool continuation belongs to another process", ErrInvalidTrajectory)
		}
		value.ContinuationOf = &behaviorEffectPosition{Step: call.ContinuationOf.Step, Index: call.ContinuationOf.Index}
	}
	if call.Settlement != nil && call.Settlement.Result != nil {
		result := call.Settlement.Result
		value.Result = &behaviorToolResult{Output: result.Output.Clone(), IsError: result.IsError}
	}
	return value, nil
}

func projectBehavior(trajectory Trajectory, project eval.Projection[run.Payload, json.RawMessage]) (behaviorProjection, error) {
	models := trajectory.ModelCalls()
	paths, err := semanticProcessPaths(trajectory.log, models)
	if err != nil {
		return behaviorProjection{}, err
	}
	projection := behaviorProjection{Termination: behaviorTerminationOf(trajectory.Termination())}
	if projection.Output, err = projectOutput(trajectory.Termination(), project); err != nil {
		return behaviorProjection{}, err
	}
	projection.foldLog(trajectory.log, paths)
	projection.sortRecords()
	slices.SortFunc(models, func(left, right ModelCall) int { return compareSemanticEffects(left.EffectID, right.EffectID, paths) })
	for _, call := range models {
		value, projectionErr := behaviorModelOf(call, paths[call.EffectID.Process])
		if projectionErr != nil {
			return behaviorProjection{}, projectionErr
		}
		projection.Models = append(projection.Models, value)
	}
	tools, err := trajectory.semanticToolCalls()
	if err != nil {
		return behaviorProjection{}, err
	}
	for _, call := range tools {
		value, projectionErr := behaviorToolOf(call, paths[call.EffectID.Process])
		if projectionErr != nil {
			return behaviorProjection{}, projectionErr
		}
		projection.Tools = append(projection.Tools, value)
	}
	return projection, nil
}

// projectOutput renders a completed output through the sample projection in
// object-key order, so equal outputs compare equal.
func projectOutput(termination run.Termination, project eval.Projection[run.Payload, json.RawMessage]) (json.RawMessage, error) {
	output, ok := termination.Output()
	if !ok {
		return nil, nil
	}
	encoded, err := project(output)
	if err != nil {
		return nil, fmt.Errorf("project output: %w", err)
	}
	value := jsontext.Value(encoded).Clone()
	if err := value.Format(jsontext.ReorderRawObjects(true)); err != nil {
		return nil, fmt.Errorf("%w: output projection must return JSON: %w", ErrInvalidSample, err)
	}
	return json.RawMessage(value), nil
}

// foldLog projects processes, steps, effects and controls from the log by
// semantic process path.
func (b *behaviorProjection) foldLog(log run.Log, paths map[run.ProcessID]string) {
	processes := make(map[run.ProcessID]behaviorProcess)
	effects := make(map[run.EffectID]behaviorEffect)
	controlCounts := make(map[run.ProcessID]uint64)
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case run.Started:
			processes[record.Process] = behaviorProcess{Path: paths[record.Process], Program: record.Program.Name()}
		case run.Exited:
			process := processes[record.Process]
			process.Termination = behaviorTerminationOf(record.Termination)
			processes[record.Process] = process
		case run.Stepped:
			b.Steps = append(b.Steps, behaviorStep{Process: paths[record.Process], Step: record.Step, Kind: record.Transition.Kind()})
		case run.EffectIntent:
			effects[record.ID] = behaviorEffect{Process: paths[record.ID.Process], Step: record.ID.Step, Index: record.ID.Index, Target: record.Effect.Target()}
		case run.EffectSettled:
			effect := effects[record.ID]
			effect.Status = record.Outcome.Status()
			effects[record.ID] = effect
		case run.ControlRequested:
			controlCounts[record.Process]++
			b.Controls = append(b.Controls, behaviorControl{Process: paths[record.Process], Sequence: controlCounts[record.Process], Kind: record.Kind})
		}
	}
	b.Processes = slices.Collect(maps.Values(processes))
	b.Effects = slices.Collect(maps.Values(effects))
}

// sortRecords puts every folded record set in its canonical order.
func (b *behaviorProjection) sortRecords() {
	slices.SortFunc(b.Processes, func(left, right behaviorProcess) int { return cmp.Compare(left.Path, right.Path) })
	slices.SortFunc(b.Steps, func(left, right behaviorStep) int {
		return cmp.Or(cmp.Compare(left.Process, right.Process), cmp.Compare(left.Step, right.Step))
	})
	slices.SortFunc(b.Effects, func(left, right behaviorEffect) int {
		return cmp.Or(cmp.Compare(left.Process, right.Process), cmp.Compare(left.Step, right.Step), cmp.Compare(left.Index, right.Index))
	})
	slices.SortFunc(b.Controls, func(left, right behaviorControl) int {
		return cmp.Or(cmp.Compare(left.Process, right.Process), cmp.Compare(left.Sequence, right.Sequence))
	})
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

type delegateAddress struct {
	parent run.ProcessID
	key    run.ChildKey
}

func semanticProcessPaths(log run.Log, models []ModelCall) (map[run.ProcessID]string, error) {
	segments := make(map[delegateAddress]string)
	ordinals := make(map[run.ProcessID]uint64)
	for _, model := range models {
		process := model.EffectID.Process
		ordinals[process]++
		for index, decision := range model.toolDecisions() {
			key, err := interaction.DelegateChildKey(model.Sequence, decision)
			if err != nil {
				return nil, err
			}
			segments[delegateAddress{process, key}] = fmt.Sprintf("@delegate/%020d/%010d", ordinals[process], index)
		}
	}
	paths := map[run.ProcessID]string{log.Root: rootProcessPath}
	for _, entry := range log.Entries {
		record, ok := entry.Record.(run.Started)
		if !ok || record.Process == log.Root {
			continue
		}
		parent, hasParent := record.Process.Parent()
		key, hasKey := record.Process.Key()
		prefix, parentExists := paths[parent]
		if !hasParent || !hasKey || !parentExists {
			return nil, fmt.Errorf("%w: process has no prior parent", ErrInvalidTrajectory)
		}
		segment := key.String()
		if semantic, ok := segments[delegateAddress{parent, key}]; ok {
			segment = semantic
		}
		paths[record.Process] = prefix + "/" + segment
	}
	return paths, nil
}

func compareSemanticEffects(left, right run.EffectID, paths map[run.ProcessID]string) int {
	if order := cmp.Compare(paths[left.Process], paths[right.Process]); order != 0 {
		return order
	}
	if order := cmp.Compare(left.Step, right.Step); order != 0 {
		return order
	}
	return cmp.Compare(left.Index, right.Index)
}
