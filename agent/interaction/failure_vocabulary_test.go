package interaction_test

import (
	"testing"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/run"
)

// Recorded logs persist these classifications, so each wire value is pinned.
func TestFailureVocabularyIsStable(t *testing.T) {
	for want, declared := range map[run.FailureClassification]run.FailureClassification{
		run.NewFailureClassification(run.FailureKindContract, "interaction.signal.invalid"):                 interaction.ErrInvalidSteer.Classification(),
		run.NewFailureClassification(run.FailureKindContract, "interaction.state.invalid"):                  interaction.ErrInvalidExecutionState.Classification(),
		run.NewFailureClassification(run.FailureKindContract, "interaction.protocol.invalid"):               interaction.ErrInvalidProtocol.Classification(),
		run.NewFailureClassification(run.FailureKindExternal, "interaction.model.invalid_response"):         interaction.ErrInvalidModelResponse.Classification(),
		run.NewFailureClassification(run.FailureKindExternal, "interaction.model.tool_calls_not_completed"): interaction.FailureToolCallsNotCompleted,
		run.NewFailureClassification(run.FailureKindExternal, "interaction.model.failed"):                   interaction.FailureModelFailed,
		run.NewFailureClassification(run.FailureKindExternal, "interaction.model.not_executed"):             interaction.FailureModelNotExecuted,
		run.NewFailureClassification(run.FailureKindExternal, "interaction.context.rejected"):               interaction.FailureContextRejected,
		run.NewFailureClassification(run.FailureKindContract, "interaction.dispatch.rejected"):              interaction.FailureDispatchRejected,
		run.NewFailureClassification(run.FailureKindContract, "interaction.tool.advertisement_invalid"):     interaction.FailureToolAdvertisementInvalid,
		run.NewFailureClassification(run.FailureKindContract, "interaction.completion.decision_invalid"):    interaction.FailureCompletionDecisionInvalid,
		run.NewFailureClassification(run.FailureKindExecution, "interaction.completion.validator_failed"):   interaction.FailureCompletionValidatorFailed,
		run.NewFailureClassification(run.FailureKindExecution, "interaction.context.policy_failed"):         interaction.FailureContextPolicyFailed,
		run.NewFailureClassification(run.FailureKindExecution, "interaction.limit.model_calls"):             interaction.FailureModelCallLimit,
		run.NewFailureClassification(run.FailureKindExecution, "interaction.limit.response_bytes"):          interaction.FailureResponseByteLimit,
	} {
		if declared != want {
			t.Errorf("declared %+v, want %+v", declared, want)
		}
	}
}
