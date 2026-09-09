package agent_test

import (
	"encoding/json"
	"testing"

	"github.com/Tangerg/scope/agent"
)

// vocabulary describes one stable enum: the values that are legal on the wire
// plus the zero value that must never be.
type vocabulary struct {
	invalid any
	valid   []any
}

func valid(value any) bool {
	type validator interface{ Valid() bool }
	checker, ok := value.(validator)
	if !ok {
		return false
	}
	return checker.Valid()
}

func text(value any) string {
	type stringer interface{ String() string }
	printer, ok := value.(stringer)
	if !ok {
		return ""
	}
	return printer.String()
}

// TestStableEnumVocabulary pins every exported enum the Engine writes into a
// snapshot or event. These strings are wire values: a rename silently
// invalidates every persisted tree, and a zero value that reports Valid would
// let an unset field travel as if it were a decision.
func TestStableEnumVocabulary(t *testing.T) {
	vocabularies := map[string]vocabulary{
		"replay policy": {
			invalid: agent.ReplayPolicyInvalid,
			valid:   []any{agent.ReplayPolicyNever, agent.ReplayPolicySameIdentity},
		},
		"effect target": {
			invalid: agent.EffectTargetInvalid,
			valid:   []any{agent.EffectTargetFramework, agent.EffectTargetDispatcher},
		},
		"event phase": {
			invalid: agent.EventPhaseInvalid,
			valid:   []any{agent.EventPhaseAttempt, agent.EventPhaseCommitted},
		},
		"settlement status": {
			invalid: agent.SettlementStatusInvalid,
			valid: []any{
				agent.SettlementStatusSucceeded,
				agent.SettlementStatusFailed,
				agent.SettlementStatusUnknown,
			},
		},
		"transition kind": {
			invalid: agent.TransitionKindInvalid,
			valid: []any{
				agent.TransitionKindContinue,
				agent.TransitionKindWait,
				agent.TransitionKindPause,
				agent.TransitionKindComplete,
				agent.TransitionKindFail,
			},
		},
		"failure kind": {
			invalid: agent.FailureKindInvalid,
			valid: []any{
				agent.FailureKindExecution,
				agent.FailureKindContract,
				agent.FailureKindExternal,
				agent.FailureKindPanic,
			},
		},
		"status": {
			invalid: agent.StatusInvalid,
			valid: []any{
				agent.StatusNotStarted,
				agent.StatusRunning,
				agent.StatusWaiting,
				agent.StatusPaused,
				agent.StatusCompleted,
				agent.StatusFailed,
				agent.StatusCanceled,
				agent.StatusTimedOut,
				agent.StatusKilled,
			},
		},
		"termination cause": {
			invalid: agent.TerminationCauseInvalid,
			valid: []any{
				agent.TerminationCauseCompletion,
				agent.TerminationCauseEngineKill,
				agent.TerminationCauseProcessDeadline,
				agent.TerminationCauseParentDeadline,
				agent.TerminationCauseHostDeadline,
				agent.TerminationCauseParentCancellation,
				agent.TerminationCauseHostCancellation,
				agent.TerminationCauseExecutionFailure,
				agent.TerminationCauseContractFailure,
				agent.TerminationCauseExternalFailure,
				agent.TerminationCausePanic,
			},
		},
		"process start outcome status": {
			invalid: agent.ProcessStartOutcomeStatusInvalid,
			valid: []any{
				agent.ProcessStartOutcomeStatusStarted,
				agent.ProcessStartOutcomeStatusAborted,
			},
		},
		"effect boundary kind": {
			invalid: agent.EffectBoundaryInvalid,
			valid: []any{
				agent.EffectBoundaryPending,
				agent.EffectBoundarySettled,
				agent.EffectBoundaryResolved,
			},
		},
		"tree checkpoint kind": {
			invalid: agent.TreeCheckpointInvalid,
			valid:   []any{agent.TreeCheckpointStart, agent.TreeCheckpointChild, agent.TreeCheckpointInput, agent.TreeCheckpointProgress, agent.TreeCheckpointParked, agent.TreeCheckpointTerminal},
		},
		"child wait boundary": {
			invalid: agent.ChildWaitBoundary(""),
			valid:   []any{agent.ChildWaitBoundaryResult, agent.ChildWaitBoundaryDrained},
		},
		"step status": {
			invalid: agent.StepStatus(""),
			valid:   []any{agent.StepStatusSucceeded, agent.StepStatusFailed},
		},
	}

	for name, enum := range vocabularies {
		t.Run(name, func(t *testing.T) {
			if valid(enum.invalid) {
				t.Errorf("the zero value reports itself valid")
			}
			if got := text(enum.invalid); got != "invalid" {
				t.Errorf("invalid value prints %q, want %q", got, "invalid")
			}
			seen := make(map[string]bool, len(enum.valid))
			for _, value := range enum.valid {
				if !valid(value) {
					t.Errorf("%v reports itself invalid", value)
					continue
				}
				printed := text(value)
				if printed == "" || printed == "invalid" {
					t.Errorf("%v prints %q", value, printed)
				}
				if seen[printed] {
					t.Errorf("wire value %q is used twice", printed)
				}
				seen[printed] = true
			}
		})
	}
}

// TestStepStatusPrintsItsWireValue is separate because StepStatus has no
// declared invalid constant: its zero value is simply not a member.
func TestStepStatusPrintsItsWireValue(t *testing.T) {
	if agent.StepStatusSucceeded.String() != "succeeded" {
		t.Errorf("StepStatusSucceeded prints %q", agent.StepStatusSucceeded)
	}
	if agent.StepStatusFailed.String() != "failed" {
		t.Errorf("StepStatusFailed prints %q", agent.StepStatusFailed)
	}
}

// TestCapabilityIsAQualifiedName keeps capability names inside the one shape
// attenuation is defined over. A capability that round-trips through text
// differently than it parsed would silently widen or narrow authority.
func TestCapabilityIsAQualifiedName(t *testing.T) {
	capability, err := agent.ParseCapability("scope.tool.invoke")
	if err != nil {
		t.Fatal(err)
	}
	if !capability.Valid() || capability.String() != "scope.tool.invoke" {
		t.Fatalf("capability = %q, valid = %t", capability, capability.Valid())
	}

	encoded, err := capability.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	var decoded agent.Capability
	if err := decoded.UnmarshalText(encoded); err != nil {
		t.Fatal(err)
	}
	if decoded != capability {
		t.Fatalf("capability round trip = %q, want %q", decoded, capability)
	}

	var zero agent.Capability
	if zero.Valid() {
		t.Error("the zero Capability reports itself valid")
	}
	if _, err := zero.MarshalText(); err == nil {
		t.Error("the zero Capability encoded without error")
	}
	if err := (*agent.Capability)(nil).UnmarshalText([]byte("scope.tool.invoke")); err == nil {
		t.Error("a nil Capability receiver accepted text")
	}
	for name, invalid := range map[string]string{
		"empty":           "",
		"uppercase":       "Scope.Tool",
		"whitespace":      "scope tool",
		"leading digit":   "1scope.tool",
		"leading dot":     ".scope.tool",
		"slash separated": "scope/tool",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := agent.ParseCapability(invalid); err == nil {
				t.Fatalf("ParseCapability(%q) succeeded", invalid)
			}
			if err := decoded.UnmarshalText([]byte(invalid)); err == nil {
				t.Fatalf("UnmarshalText(%q) succeeded", invalid)
			}
		})
	}
}

// TestEnumsSurviveJSON proves the wire vocabulary is what actually reaches a
// snapshot, not just what String reports.
func TestEnumsSurviveJSON(t *testing.T) {
	type envelope struct {
		Status      agent.Status           `json:"status"`
		Transition  agent.TransitionKind   `json:"transition"`
		Termination agent.TerminationCause `json:"termination"`
	}
	original := envelope{
		Status:      agent.StatusWaiting,
		Transition:  agent.TransitionKindWait,
		Termination: agent.TerminationCauseHostCancellation,
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"status":"waiting","transition":"wait","termination":"host_cancellation"}`
	if string(encoded) != want {
		t.Fatalf("encoded = %s, want %s", encoded, want)
	}

	var decoded envelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != original {
		t.Fatalf("round trip = %#v, want %#v", decoded, original)
	}
}
