package trajectory

import (
	"encoding/json"
	"encoding/json/jsontext"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

type behaviorProjection struct {
	Termination behaviorTermination `json:"termination"`
	Output      *agent.Output       `json:"output,omitempty"`
	Events      []behaviorEvent     `json:"events"`
	Models      []behaviorModel     `json:"models,omitempty"`
	Tools       []behaviorTool      `json:"tools,omitempty"`
}

type behaviorTermination struct {
	Status            agent.Status           `json:"status"`
	Cause             agent.TerminationCause `json:"cause"`
	Reason            string                 `json:"reason,omitempty"`
	FailureKind       agent.FailureKind      `json:"failure_kind,omitempty"`
	FailureCode       string                 `json:"failure_code,omitempty"`
	FailureMessage    string                 `json:"failure_message,omitempty"`
	UnresolvedEffects int                    `json:"unresolved_effects,omitempty"`
}

type behaviorEvent struct {
	ProcessPath      string                 `json:"process_path"`
	Sequence         uint64                 `json:"sequence"`
	StepSequence     uint64                 `json:"step_sequence,omitempty"`
	Name             string                 `json:"name"`
	Phase            agent.EventPhase       `json:"phase"`
	ProcessStatus    agent.Status           `json:"process_status,omitempty"`
	TerminationCause agent.TerminationCause `json:"termination_cause,omitempty"`
	FailureKind      agent.FailureKind      `json:"failure_kind,omitempty"`
	FailureCode      string                 `json:"failure_code,omitempty"`
	StepStatus       agent.StepStatus       `json:"step_status,omitempty"`
	EffectTarget     agent.EffectTarget     `json:"effect_target,omitempty"`
	Settlement       agent.SettlementStatus `json:"settlement,omitempty"`
	DroppedDeltas    uint64                 `json:"dropped_deltas,omitempty"`
}

type behaviorModel struct {
	ProcessPath string `json:"process_path"`
	Step        uint64 `json:"step"`
	Sequence    uint32 `json:"sequence"`
}

type behaviorTool struct {
	ProcessPath string              `json:"process_path"`
	Step        uint64              `json:"step"`
	ModelCall   uint32              `json:"model_call"`
	Index       uint32              `json:"index"`
	Name        string              `json:"name"`
	Arguments   json.RawMessage     `json:"arguments,omitempty"`
	Outcome     ToolOutcome         `json:"outcome"`
	Result      *behaviorToolResult `json:"result,omitempty"`
}

type behaviorToolResult struct {
	Name    string          `json:"name"`
	Output  chat.ToolOutput `json:"output"`
	IsError bool            `json:"is_error,omitempty"`
}

func behaviorTerminationOf(termination agent.Termination) behaviorTermination {
	projection := behaviorTermination{
		Status: termination.Status(), Cause: termination.Cause(), Reason: termination.Reason(),
		UnresolvedEffects: len(termination.UnresolvedEffectIDs()),
	}
	if failure, present := termination.Failure(); present {
		projection.FailureKind = failure.Kind()
		projection.FailureCode = failure.Code()
		projection.FailureMessage = failure.Message()
	}
	return projection
}

func (b *behaviorEvent) apply(event agent.Event) {
	if fact, present := event.ProcessFinished(); present {
		failureKind, failureCode, failed := fact.Failure()
		b.ProcessStatus = fact.Status()
		b.TerminationCause = fact.Cause()
		if failed {
			b.FailureKind = failureKind
			b.FailureCode = failureCode
		}
		return
	}
	if fact, present := event.StepFinished(); present {
		b.StepStatus = fact.Status()
		return
	}
	if fact, present := event.StepCommitted(); present {
		b.ProcessStatus = fact.Status()
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
	if fact, present := event.DeltaDropped(); present {
		b.DroppedDeltas = fact.Count()
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
