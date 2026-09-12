package trajectory

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/eval"
)

const (
	rootProcessPath      = "root"
	processPathSeparator = "/"
)

// Trajectory is an owned, portable record of one completed root Process tree.
// Absolute timing and provider responses remain available in the record, but
// BehaviorDigest uses an explicit Host output projection for replay comparison.
// Root termination and usage must agree with the root Process's finished Event,
// including its termination cause and stable failure classification.
type Trajectory struct {
	rootProcessID agent.ProcessID
	termination   agent.Termination
	output        *agent.Output
	rootUsage     agent.Usage
	coverage      *Coverage
	elapsed       *time.Duration
	events        []agent.Event
	modelCalls    []ModelCall
	toolCalls     []ToolCall
}

// New clones every input and then sorts by process path rather than by arrival
// time. Two runs of the same agent interleave concurrent siblings differently,
// so wall-clock order would make identical behavior compare as a regression;
// ordering by structural position is what makes replay comparison meaningful.
func New(config Config) (Trajectory, error) {
	trajectory := Trajectory{
		rootProcessID: config.RootProcessID,
		termination:   config.Termination,
		output:        cloneOutput(config.Output),
		rootUsage:     config.RootUsage,
		coverage:      config.Coverage.clone(),
		elapsed:       cloneElapsed(config.Elapsed),
		events:        slices.Clone(config.Events),
		modelCalls:    cloneModelCalls(config.ModelCalls),
		toolCalls:     cloneToolCalls(config.ToolCalls),
	}
	if err := trajectory.canonicalize(); err != nil {
		return Trajectory{}, err
	}
	if err := trajectory.Validate(); err != nil {
		return Trajectory{}, err
	}
	return trajectory, nil
}

// Config supplies the complete facts owned by one Trajectory.
type Config struct {
	RootProcessID agent.ProcessID
	Termination   agent.Termination
	Output        *agent.Output
	RootUsage     agent.Usage
	Coverage      *Coverage
	Elapsed       *time.Duration
	Events        []agent.Event
	ModelCalls    []ModelCall
	ToolCalls     []ToolCall
}

func (t Trajectory) Clone() (Trajectory, error) {
	return New(t.config())
}

func (t Trajectory) RootProcessID() agent.ProcessID { return t.rootProcessID }

func (t Trajectory) Termination() agent.Termination { return t.termination }

func (t Trajectory) Output() *agent.Output { return cloneOutput(t.output) }

func (t Trajectory) RootUsage() agent.Usage { return t.rootUsage }

func (t Trajectory) Coverage() *Coverage { return t.coverage.clone() }

// Elapsed is the monotonic recording interval from root start to export,
// including idle time. Nil means that the complete interval was not observed.
func (t Trajectory) Elapsed() *time.Duration { return cloneElapsed(t.elapsed) }

func (t Trajectory) Events() []agent.Event { return slices.Clone(t.events) }

func (t Trajectory) ModelCalls() []ModelCall { return cloneModelCalls(t.modelCalls) }

func (t Trajectory) ToolCalls() []ToolCall { return cloneToolCalls(t.toolCalls) }

func (t Trajectory) config() Config {
	return Config{
		RootProcessID: t.rootProcessID, Termination: t.termination,
		Output: t.output, RootUsage: t.rootUsage, Coverage: t.coverage, Elapsed: t.elapsed,
		Events: t.events, ModelCalls: t.modelCalls, ToolCalls: t.toolCalls,
	}
}

type trajectoryWire struct {
	RootProcessID agent.ProcessID   `json:"root_process_id"`
	Termination   agent.Termination `json:"termination"`
	Output        *agent.Output     `json:"output,omitempty"`
	RootUsage     agent.Usage       `json:"root_usage"`
	Coverage      *Coverage         `json:"coverage,omitempty"`
	Elapsed       *time.Duration    `json:"elapsed_ns,omitempty"`
	Events        []agent.Event     `json:"events"`
	ModelCalls    []ModelCall       `json:"model_calls,omitempty"`
	ToolCalls     []ToolCall        `json:"tool_calls,omitempty"`
}

func (t Trajectory) MarshalJSON() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(trajectoryWire{
		RootProcessID: t.rootProcessID, Termination: t.termination,
		Output: t.output, RootUsage: t.rootUsage, Coverage: t.coverage, Elapsed: t.elapsed,
		Events: t.events, ModelCalls: t.modelCalls, ToolCalls: t.toolCalls,
	})
}

func (t *Trajectory) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidTrajectory)
	}
	var decoded trajectoryWire
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true), json.FormatDurationAsNano(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidTrajectory, err)
	}
	canonical, err := New(Config(decoded))
	if err != nil {
		return err
	}
	*t = canonical
	return nil
}

func (t Trajectory) Validate() error {
	if !t.rootProcessID.Valid() || !t.termination.Valid() || (t.elapsed != nil && *t.elapsed < 0) {
		return fmt.Errorf("%w: root outcome is incomplete", ErrInvalidTrajectory)
	}
	if t.termination.Status() == agent.StatusCompleted {
		if t.output == nil || !t.output.Valid() {
			return fmt.Errorf("%w: completed trajectory requires output", ErrInvalidTrajectory)
		}
	} else if t.output != nil {
		return fmt.Errorf("%w: non-completed trajectory cannot carry output", ErrInvalidTrajectory)
	}
	if len(t.events) == 0 {
		return fmt.Errorf("%w: at least one agent event is required", ErrInvalidTrajectory)
	}
	paths, err := processPaths(t.rootProcessID, t.events)
	if err != nil {
		return err
	}
	if !slices.IsSortedFunc(t.events, func(left, right agent.Event) int {
		return compareEvent(left, right, paths)
	}) {
		return fmt.Errorf("%w: events are not in canonical process order", ErrInvalidTrajectory)
	}
	finished := 0
	terminals := make(map[agent.ProcessID]bool)
	sequences := make(map[activationProcess]uint64)
	for index, event := range t.events {
		if !event.Valid() || event.Relation().RootID() != t.rootProcessID {
			return fmt.Errorf("%w: events[%d] is invalid or belongs to another tree", ErrInvalidTrajectory, index)
		}
		want := sequences[activationProcess{event.ProcessID(), eventIncarnation(event)}] + 1
		if event.ProcessSequence() != want {
			return fmt.Errorf("%w: events[%d] breaks process-local order", ErrInvalidTrajectory, index)
		}
		sequences[activationProcess{event.ProcessID(), eventIncarnation(event)}] = want
		if event.Name() == agent.EventProcessFinished {
			if terminals[event.ProcessID()] {
				return fmt.Errorf("%w: duplicate process terminal event", ErrInvalidTrajectory)
			}
			terminals[event.ProcessID()] = true
		}
		if event.ProcessID() == t.rootProcessID && event.Name() == agent.EventProcessFinished {
			fact, present := event.ProcessFinished()
			if !present || !t.matchesRootOutcome(fact) {
				return fmt.Errorf("%w: root finished event disagrees with outcome", ErrInvalidTrajectory)
			}
			finished++
		}
	}
	for process := range paths {
		if !terminals[process] {
			return fmt.Errorf("%w: process %s has no terminal evidence", ErrIncompleteRecording, process)
		}
	}
	if finished != 1 {
		return fmt.Errorf("%w: root must have exactly one finished event", ErrInvalidTrajectory)
	}
	for index, call := range t.modelCalls {
		if err := call.Validate(); err != nil {
			return fmt.Errorf("%w: model_calls[%d]: %w", ErrInvalidTrajectory, index, err)
		}
		if _, present := paths[call.ProcessID]; !present {
			return fmt.Errorf("%w: model_calls[%d] belongs to another tree", ErrInvalidTrajectory, index)
		}
		if index > 0 && compareModelCall(t.modelCalls[index-1], call, paths) >= 0 {
			return fmt.Errorf("%w: model_calls must have unique canonical attribution", ErrInvalidTrajectory)
		}
	}
	for index, call := range t.toolCalls {
		if err := call.Validate(); err != nil {
			return fmt.Errorf("%w: tool_calls[%d]: %w", ErrInvalidTrajectory, index, err)
		}
		if _, present := paths[call.ProcessID]; !present {
			return fmt.Errorf("%w: tool_calls[%d] belongs to another tree", ErrInvalidTrajectory, index)
		}
		if index > 0 && compareToolCall(t.toolCalls[index-1], call, paths) >= 0 {
			return fmt.Errorf("%w: tool_calls must have unique canonical attribution", ErrInvalidTrajectory)
		}
	}
	if t.coverage != nil {
		return t.validateCoverage()
	}
	return nil
}

func (t Trajectory) matchesRootOutcome(fact agent.ProcessFinishedFact) bool {
	if fact.Status() != t.termination.Status() || fact.Cause() != t.termination.Cause() || fact.Usage() != t.rootUsage {
		return false
	}
	failure, failed := t.termination.Failure()
	if !failed {
		return true
	}
	kind, code, _ := fact.Failure()
	return kind == failure.Kind() && code == failure.Code()
}

func (t Trajectory) TotalTokens() (int64, error) {
	if err := t.Validate(); err != nil {
		return 0, err
	}
	if !t.HistoryComplete() {
		return 0, fmt.Errorf("%w: model history is incomplete", ErrIncompleteRecording)
	}
	if err := t.validateCoverage(); err != nil {
		return 0, err
	}
	var total int64
	for _, call := range t.modelCalls {
		if call.Response.Metadata == nil || call.Response.Metadata.Usage == nil {
			return 0, fmt.Errorf("%w: model token accounting is absent", ErrIncompleteRecording)
		}
		value := call.Response.Metadata.Usage.TotalTokens()
		if value > (1<<63-1)-total {
			return 0, fmt.Errorf("%w: total model tokens overflow int64", ErrInvalidTrajectory)
		}
		total += value
	}
	return total, nil
}

func (t *Trajectory) canonicalize() error {
	paths, err := processPaths(t.rootProcessID, t.events)
	if err != nil {
		return err
	}
	slices.SortFunc(t.events, func(left, right agent.Event) int {
		return compareEvent(left, right, paths)
	})
	slices.SortFunc(t.modelCalls, func(left, right ModelCall) int {
		return compareModelCall(left, right, paths)
	})
	slices.SortFunc(t.toolCalls, func(left, right ToolCall) int {
		return compareToolCall(left, right, paths)
	})
	return nil
}

// BehaviorDigest identifies deterministic, semantic behavior while excluding
// observation loss, signal arrival interleaving, transient scheduling status,
// wall-clock time, attempt duration, response envelopes, and token usage.
// The required projection selects the semantic root output; the generic
// recorder never guesses which opaque output fields are business data.
// Complete event history and declared semantic coverage are required.
func (t Trajectory) BehaviorDigest(project eval.Projection[agent.Output, json.RawMessage]) (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	if !t.HistoryComplete() {
		return "", fmt.Errorf("%w: replay history is incomplete", ErrIncompleteRecording)
	}
	if err := t.validateCoverage(); err != nil {
		return "", err
	}
	if project == nil {
		return "", fmt.Errorf("%w: output projection is required", ErrInvalidSample)
	}
	projection, err := t.behavior(project)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return "", fmt.Errorf("%w: encode behavior: %w", ErrInvalidTrajectory, err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (t Trajectory) behavior(project eval.Projection[agent.Output, json.RawMessage]) (behaviorProjection, error) {
	paths, err := processPaths(t.rootProcessID, t.events)
	if err != nil {
		return behaviorProjection{}, err
	}
	projection := behaviorProjection{
		Termination: behaviorTerminationOf(t.termination),
	}
	if t.output != nil {
		projection.Output, err = project(*t.output)
		if err != nil {
			return behaviorProjection{}, fmt.Errorf("project output: %w", err)
		}
		if len(projection.Output) == 0 || !json.Valid(projection.Output) {
			return behaviorProjection{}, fmt.Errorf("%w: projection must return JSON", ErrInvalidSample)
		}
	}
	sequences := make(map[agent.ProcessID]uint64)
	projection.Events = make([]behaviorEvent, 0, len(t.events))
	for _, event := range t.events {
		if event.Name() == agent.EventDeltaDropped || event.Name() == agent.EventSignalAccepted {
			continue
		}
		sequences[event.ProcessID()]++
		step, _ := event.StepSequence()
		fact := behaviorEvent{
			ProcessPath: paths[event.ProcessID()], Sequence: sequences[event.ProcessID()],
			StepSequence: step, Name: event.Name(), Phase: event.Phase(),
		}
		fact.apply(event)
		projection.Events = append(projection.Events, fact)
	}
	projection.Models = make([]behaviorModel, len(t.modelCalls))
	for index, call := range t.modelCalls {
		projection.Models[index] = behaviorModel{
			ProcessPath: paths[call.ProcessID], Step: call.StepSequence,
			Sequence: call.CallSequence,
		}
	}
	projection.Tools = make([]behaviorTool, len(t.toolCalls))
	for index, call := range t.toolCalls {
		arguments, err := canonicalArguments(call.Call.Arguments)
		if err != nil {
			return behaviorProjection{}, err
		}
		var result *behaviorToolResult
		if call.Result != nil {
			cloned := call.Result.Clone()
			result = &behaviorToolResult{Name: cloned.Name, Output: cloned.Output, IsError: cloned.IsError}
		}
		projection.Tools[index] = behaviorTool{
			ProcessPath: paths[call.ProcessID], Step: call.StepSequence,
			ModelCall: call.ModelCall, Index: call.Index, Name: call.Call.Name,
			Arguments: arguments, Outcome: call.Outcome, Result: result,
		}
	}
	return projection, nil
}

func (t Trajectory) consistencyReport(baseline Trajectory, project eval.Projection[agent.Output, json.RawMessage]) (eval.Report, error) {
	actualDigest, err := t.BehaviorDigest(project)
	if err != nil {
		return eval.Report{}, err
	}
	baselineDigest, err := baseline.BehaviorDigest(project)
	if err != nil {
		return eval.Report{}, err
	}
	passed := actualDigest == baselineDigest
	feedback := "semantic trajectory matched the replay baseline"
	if !passed {
		feedback = "semantic trajectory differed from the replay baseline"
	}
	return binaryReport(MetricConsistency, passed, feedback)
}

func compareEvent(left, right agent.Event, paths map[agent.ProcessID]string) int {
	if result := strings.Compare(paths[left.ProcessID()], paths[right.ProcessID()]); result != 0 {
		return result
	}
	if order := strings.Compare(eventIncarnation(left).String(), eventIncarnation(right).String()); order != 0 {
		return order
	}
	return cmp.Compare(left.ProcessSequence(), right.ProcessSequence())
}

func compareModelCall(left, right ModelCall, paths map[agent.ProcessID]string) int {
	if result := strings.Compare(paths[left.ProcessID], paths[right.ProcessID]); result != 0 {
		return result
	}
	if order := strings.Compare(left.TreeIncarnationID.String(), right.TreeIncarnationID.String()); order != 0 {
		return order
	}
	if left.StepSequence != right.StepSequence {
		return cmp.Compare(left.StepSequence, right.StepSequence)
	}
	return cmp.Compare(left.CallSequence, right.CallSequence)
}

func compareToolCall(left, right ToolCall, paths map[agent.ProcessID]string) int {
	if result := strings.Compare(paths[left.ProcessID], paths[right.ProcessID]); result != 0 {
		return result
	}
	if order := strings.Compare(left.TreeIncarnationID.String(), right.TreeIncarnationID.String()); order != 0 {
		return order
	}
	if left.StepSequence != right.StepSequence {
		return cmp.Compare(left.StepSequence, right.StepSequence)
	}
	if left.ModelCall != right.ModelCall {
		return cmp.Compare(left.ModelCall, right.ModelCall)
	}
	return cmp.Compare(left.Index, right.Index)
}

func processPaths(root agent.ProcessID, events []agent.Event) (map[agent.ProcessID]string, error) {
	if !root.Valid() || len(events) == 0 {
		return nil, fmt.Errorf("%w: process relations are incomplete", ErrInvalidTrajectory)
	}
	relations := make(map[agent.ProcessID]agent.ProcessRelation)
	for _, event := range events {
		if !event.Valid() || event.Relation().RootID() != root {
			return nil, fmt.Errorf("%w: event process relation is invalid", ErrInvalidTrajectory)
		}
		if previous, present := relations[event.ProcessID()]; present && previous != event.Relation() {
			return nil, fmt.Errorf("%w: process relation changed within one trajectory", ErrInvalidTrajectory)
		}
		relations[event.ProcessID()] = event.Relation()
	}
	rootRelation, present := relations[root]
	if !present || !rootRelation.IsRoot() {
		return nil, fmt.Errorf("%w: root process relation is missing", ErrInvalidTrajectory)
	}
	paths := map[agent.ProcessID]string{root: rootProcessPath}
	pathOwners := map[string]agent.ProcessID{rootProcessPath: root}
	for len(paths) < len(relations) {
		progress := false
		for processID, relation := range relations {
			if _, resolved := paths[processID]; resolved {
				continue
			}
			parentID, hasParent := relation.ParentID()
			parentPath, parentResolved := paths[parentID]
			childKey, hasChildKey := relation.ChildKey()
			if !hasParent || !hasChildKey || !parentResolved {
				continue
			}
			path := parentPath + processPathSeparator + childKey.String()
			if owner, duplicate := pathOwners[path]; duplicate && owner != processID {
				return nil, fmt.Errorf("%w: process relation path %q is duplicated", ErrInvalidTrajectory, path)
			}
			paths[processID] = path
			pathOwners[path] = processID
			progress = true
		}
		if !progress {
			return nil, fmt.Errorf("%w: process relations do not form one rooted tree", ErrInvalidTrajectory)
		}
	}
	return paths, nil
}

func cloneOutput(output *agent.Output) *agent.Output {
	if output == nil {
		return nil
	}
	clone := *output
	return &clone
}

func cloneModelCalls(calls []ModelCall) []ModelCall {
	cloned := slices.Clone(calls)
	for index := range cloned {
		cloned[index] = cloned[index].Clone()
	}
	return cloned
}

func cloneToolCalls(calls []ToolCall) []ToolCall {
	cloned := slices.Clone(calls)
	for index := range cloned {
		cloned[index] = cloned[index].Clone()
	}
	return cloned
}

func cloneElapsed(value *time.Duration) *time.Duration {
	if value == nil {
		return nil
	}
	return new(*value)
}

type activationProcess struct {
	process     agent.ProcessID
	incarnation agent.TreeIncarnationID
}
type observedEffect struct {
	process     agent.ProcessID
	incarnation agent.TreeIncarnationID
	effect      agent.EffectID
}

func eventIncarnation(event agent.Event) agent.TreeIncarnationID {
	value, _ := event.TreeIncarnationID()
	return value
}

// HistoryComplete reports whether every observed Process starts in this
// recording. A restored activation is conservatively a history fragment:
// neither event continuity nor a final result proves pre-restart coverage.
func (t Trajectory) HistoryComplete() bool {
	for _, event := range t.events {
		if event.Name() == agent.EventProcessRestored || event.Name() == agent.EventRuntimeStopped {
			return false
		}
		if event.ProcessSequence() == 1 && event.Name() != agent.EventProcessStarted {
			return false
		}
	}
	return len(t.events) > 0
}

// TreeUsage sums final cumulative usage once per logical Process. RootUsage
// remains available independently. Incomplete historical evidence is an error.
func (t Trajectory) TreeUsage() (agent.Usage, error) {
	if err := t.Validate(); err != nil {
		return agent.Usage{}, err
	}
	if !t.HistoryComplete() {
		return agent.Usage{}, fmt.Errorf("%w: tree history is incomplete", ErrIncompleteRecording)
	}
	var total agent.Usage
	for _, event := range t.events {
		fact, ok := event.ProcessFinished()
		if !ok {
			continue
		}
		usage := fact.Usage()
		for _, pair := range []struct {
			sum   *uint64
			value uint64
		}{
			{&total.CommittedSteps, usage.CommittedSteps}, {&total.PreparedEffects, usage.PreparedEffects},
			{&total.AcceptedSignals, usage.AcceptedSignals}, {&total.DroppedDeltas, usage.DroppedDeltas},
		} {
			if pair.value > ^uint64(0)-*pair.sum {
				return agent.Usage{}, fmt.Errorf("%w: tree usage overflows uint64", ErrInvalidTrajectory)
			}
			*pair.sum += pair.value
		}
	}
	return total, nil
}

func (t Trajectory) validateCoverage() error {
	if t.coverage == nil {
		return fmt.Errorf("%w: semantic coverage is undeclared", ErrIncompleteRecording)
	}
	if err := t.coverage.Validate(); err != nil {
		return err
	}
	models := make(map[observedEffect]int)
	tools := make(map[observedEffect]int)
	for _, call := range t.modelCalls {
		models[observedEffect{call.ProcessID, call.TreeIncarnationID, call.EffectID}]++
	}
	for _, call := range t.toolCalls {
		tools[observedEffect{call.ProcessID, call.TreeIncarnationID, call.EffectID}]++
	}
	for _, event := range t.events {
		fact, ok := event.EffectStarted()
		if !ok || fact.Target() != agent.EffectTargetDispatcher {
			continue
		}
		effect, _ := event.EffectID()
		key := observedEffect{event.ProcessID(), eventIncarnation(event), effect}
		reference := event.DeploymentRef()
		switch {
		case slices.Contains(t.coverage.Models, reference):
			if models[key] != 1 {
				return fmt.Errorf("%w: model effect %s lacks exactly one response", ErrIncompleteRecording, effect)
			}
			delete(models, key)
		case slices.Contains(t.coverage.Tools, reference):
			if tools[key] != 1 {
				return fmt.Errorf("%w: tool effect %s lacks exactly one call", ErrIncompleteRecording, effect)
			}
			delete(tools, key)
		case slices.Contains(t.coverage.Other, reference):
		default:
			return fmt.Errorf("%w: dispatcher deployment %s is unclassified", ErrIncompleteRecording, reference.Name())
		}
	}
	if len(models) != 0 || len(tools) != 0 {
		return fmt.Errorf("%w: semantic observations have no matching dispatcher attempt", ErrIncompleteRecording)
	}
	return nil
}
