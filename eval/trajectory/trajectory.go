package trajectory

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/eval"
)

// Log owns every runtime fact. Coverage is an independent host declaration of
// opaque dispatcher protocols; no outcome, usage, or call can be supplied beside
// the log and disagree with it.
type Config struct {
	Log      run.Log   `json:"log"`
	Coverage *Coverage `json:"coverage,omitzero"`
}

// Admission validates the owned log once. Detached accessors cannot invalidate
// it, so evaluation never replays the same history merely to check a value.
type Trajectory struct {
	log        run.Log
	coverage   *Coverage
	inspection run.TreeInspection
	calls      callProjection
}

func New(config Config) (Trajectory, error) {
	owned, err := config.Log.Clone()
	if err != nil {
		return Trajectory{}, fmt.Errorf("%w: log: %w", ErrInvalidTrajectory, err)
	}
	inspection, err := owned.Inspect()
	if err != nil {
		return Trajectory{}, fmt.Errorf("%w: log: %w", ErrInvalidTrajectory, err)
	}
	coverage := config.Coverage.clone()
	calls, err := projectCalls(owned, coverage)
	if err != nil {
		return Trajectory{}, err
	}
	return Trajectory{log: owned, coverage: coverage, inspection: inspection, calls: calls}, nil
}

func (t Trajectory) Validate() error {
	if !t.log.Root.Valid() {
		return ErrInvalidTrajectory
	}
	return nil
}

func (t Trajectory) MarshalJSON() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return jsonv2.Marshal(Config{Log: t.log, Coverage: t.coverage})
}
func (t *Trajectory) UnmarshalJSON(data []byte) error {
	if t == nil {
		return ErrInvalidTrajectory
	}
	var config Config
	if err := jsonv2.Unmarshal(data, &config, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidTrajectory, err)
	}
	value, err := New(config)
	if err != nil {
		return err
	}
	*t = value
	return nil
}

func (t Trajectory) Log() (run.Log, error) {
	if err := t.Validate(); err != nil {
		return run.Log{}, err
	}
	return t.log.Clone()
}
func (t Trajectory) RootProcessID() run.ProcessID { return t.log.Root }
func (t Trajectory) Coverage() *Coverage          { return t.coverage.clone() }
func (t Trajectory) Termination() run.Termination {
	root, _ := t.inspection.Process(t.log.Root)
	return root.Termination
}
func (t Trajectory) Elapsed() (time.Duration, bool) {
	root, ok := t.inspection.Process(t.log.Root)
	if !ok || root.StartedAt.IsZero() || root.FinishedAt.IsZero() || root.FinishedAt.Before(root.StartedAt) {
		return 0, false
	}
	return root.FinishedAt.Sub(root.StartedAt), true
}
func (t Trajectory) RootUsage() run.Usage { return logUsage(t.log, t.log.Root) }
func (t Trajectory) ModelCalls() []ModelCall {
	calls := make([]ModelCall, 0, len(t.calls.models))
	for _, call := range t.calls.models {
		calls = append(calls, call.Clone())
	}
	slices.SortFunc(calls, func(left, right ModelCall) int { return compareEffects(left.EffectID, right.EffectID) })
	return calls
}
func (t Trajectory) ToolCalls() []ToolCall {
	calls := make([]ToolCall, 0, len(t.calls.tools))
	for _, call := range t.calls.tools {
		calls = append(calls, call.Clone())
	}
	slices.SortFunc(calls, func(left, right ToolCall) int { return compareEffects(left.EffectID, right.EffectID) })
	return calls
}

// A root exit is published only after its descendants and effects settle.
// Recovery changes Epoch, never the completeness of retained log history.
func (t Trajectory) HistoryComplete() bool { return t.Termination().Valid() }

func (t Trajectory) TreeUsage() (run.Usage, error) {
	if err := t.Validate(); err != nil {
		return run.Usage{}, err
	}
	if !t.HistoryComplete() {
		return run.Usage{}, fmt.Errorf("%w: root is not terminal", ErrIncompleteRecording)
	}
	return logUsage(t.log, run.ProcessID{}), nil
}

func (t Trajectory) CheckCoverage() error {
	if err := t.Validate(); err != nil {
		return err
	}
	if !t.HistoryComplete() {
		return fmt.Errorf("%w: root is not terminal", ErrIncompleteRecording)
	}
	if t.coverage == nil {
		return fmt.Errorf("%w: semantic coverage is undeclared", ErrIncompleteRecording)
	}
	roles, err := t.coverage.classifications()
	if err != nil {
		return err
	}
	for _, entry := range t.log.Entries {
		record, ok := entry.Record.(run.EffectIntent)
		if !ok || record.Effect.Target() != run.EffectTargetDispatcher {
			continue
		}
		if roles[record.ID] == effectRoleInvalid {
			return fmt.Errorf("%w: dispatcher effect %s is unclassified", ErrIncompleteRecording, record.ID)
		}
	}
	for _, call := range t.calls.models {
		if call.Settlement == nil {
			return fmt.Errorf("%w: model effect %s is pending", ErrIncompleteRecording, call.EffectID)
		}
	}
	for _, call := range t.calls.tools {
		if call.Settlement == nil {
			return fmt.Errorf("%w: tool effect %s is pending", ErrIncompleteRecording, call.EffectID)
		}
	}
	return nil
}

func (t Trajectory) TotalTokens() (int64, error) {
	if err := t.CheckCoverage(); err != nil {
		return 0, err
	}
	var total int64
	for _, call := range t.calls.models {
		settlement := call.Settlement
		if settlement.Outcome == interaction.ModelOutcomeNotExecuted {
			continue
		}
		if settlement.Response == nil || settlement.Response.Metadata == nil || settlement.Response.Metadata.Usage == nil {
			return 0, fmt.Errorf("%w: model token accounting is absent", ErrIncompleteRecording)
		}
		value := settlement.Response.Metadata.Usage.TotalTokens()
		if value > math.MaxInt64-total {
			return 0, fmt.Errorf("%w: total model tokens overflow int64", ErrInvalidTrajectory)
		}
		total += value
	}
	return total, nil
}

// BehaviorDigest excludes provider identifiers, timestamps, writer epochs,
// sibling publication order, and token accounting. The output projection must
// select application meaning from the otherwise opaque root output.
func (t Trajectory) BehaviorDigest(project eval.Projection[run.Payload, json.RawMessage]) (string, error) {
	if err := t.CheckCoverage(); err != nil {
		return "", err
	}
	if project == nil {
		return "", fmt.Errorf("%w: output projection is required", ErrInvalidSample)
	}
	projection, err := projectBehavior(t, project)
	if err != nil {
		return "", err
	}
	encoded, err := jsonv2.Marshal(projection, jsonv2.Deterministic(true))
	if err != nil {
		return "", fmt.Errorf("%w: encode behavior: %w", ErrInvalidTrajectory, err)
	}
	canonical := jsontext.Value(encoded)
	if err := canonical.Format(jsontext.ReorderRawObjects(true)); err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func (t Trajectory) semanticToolCalls() ([]ToolCall, error) {
	if err := t.CheckCoverage(); err != nil {
		return nil, err
	}
	paths, err := semanticProcessPaths(t.log, t.ModelCalls())
	if err != nil {
		return nil, err
	}
	calls := t.ToolCalls()
	slices.SortFunc(calls, func(left, right ToolCall) int { return compareSemanticEffects(left.EffectID, right.EffectID, paths) })
	return calls, nil
}

func (t Trajectory) consistencyReport(baseline Trajectory, project eval.Projection[run.Payload, json.RawMessage]) (eval.Report, error) {
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
	parameters := metadata.Map{}
	if err := parameters.Set("baseline_digest", baselineDigest); err != nil {
		return eval.Report{}, err
	}
	return binaryReport(MetricConsistency, passed, feedback, parameters)
}

func compareEffects(left, right run.EffectID) int {
	if order := cmp.Compare(left.Process.String(), right.Process.String()); order != 0 {
		return order
	}
	if order := cmp.Compare(left.Step, right.Step); order != 0 {
		return order
	}
	return cmp.Compare(left.Index, right.Index)
}

func logUsage(log run.Log, process run.ProcessID) run.Usage {
	var usage run.Usage
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case run.Stepped:
			if !process.Valid() || record.Process == process {
				usage.CommittedSteps++
			}
		case run.EffectIntent:
			if !process.Valid() || record.ID.Process == process {
				usage.PreparedEffects++
			}
		case run.SignalAdmitted:
			if !process.Valid() || record.Process == process {
				usage.AcceptedMessages++
			}
		case run.EffectSettled:
			if record.Outcome.Status() != run.SettlementStatusUnknown && (!process.Valid() || record.ID.Process == process) {
				usage.AcceptedMessages++
			}
		case run.Exited:
			if parent, ok := record.Process.Parent(); ok && (!process.Valid() || parent == process) {
				usage.AcceptedMessages++
			}
		}
	}
	return usage
}
