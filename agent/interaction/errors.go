package interaction

import (
	"errors"

	"github.com/Tangerg/scope/agent/run"
)

// These sentinels belong to construction, domain values, dispatcher operations,
// and Tool control flow rather than Steps, so they carry no Failure
// classification.
var (
	ErrInvalidResult            = errors.New("interaction: invalid result")
	ErrInvalidModelSettlement   = errors.New("interaction: invalid model settlement")
	ErrInvalidToolAdvertisement = errors.New("interaction: invalid tool advertisement")
	ErrInvalidPendingToolInput  = errors.New("interaction: invalid pending tool input")
	ErrInvalidConfig            = errors.New("interaction: invalid configuration")
	ErrInvalidToolSet           = errors.New("interaction: invalid tool set")
	ErrInvalidDelegate          = errors.New("interaction: invalid delegate")
	ErrInvalidInput             = errors.New("interaction: invalid input")

	ErrInvalidCompletionDecision = errors.New("interaction: invalid completion decision")
)

// ErrModelResponseTooLarge rejects a canonical settlement that exceeds its
// budget. Complete generation remains observable, but its Effect stays unknown.
// Oversize replacement context fails before the model is called.
var ErrModelResponseTooLarge = errors.New("interaction: model response exceeds byte limit")

// ErrModelStreamTooLarge stops consumption before an encoded delta exceeds the
// stream admission budget. Observed generation cannot establish non-execution.
var ErrModelStreamTooLarge = errors.New("interaction: encoded model response deltas exceed stream byte limit")

// Execution sentinels own the Failure persisted when they reach Step. Wrapping
// one is the only way a Step error acquires its classification. Protocol and
// restored-state errors stay distinct because their recovery responsibilities
// differ.
var (
	ErrInvalidSteer = run.NewClassifiedError(
		run.FailureKindContract,
		"interaction.signal.invalid",
		"interaction: invalid steer",
	)
	ErrInvalidExecutionState = run.NewClassifiedError(
		run.FailureKindContract,
		"interaction.state.invalid",
		"interaction: invalid execution state",
	)
	ErrInvalidProtocol = run.NewClassifiedError(
		run.FailureKindContract,
		"interaction.protocol.invalid",
		"interaction: invalid protocol payload",
	)
	// ErrInvalidModelResponse rejects the whole Step, so a structurally valid
	// response that violates Interaction constraints never enters its state.
	ErrInvalidModelResponse = run.NewClassifiedError(
		run.FailureKindExternal,
		"interaction.model.invalid_response",
		"interaction: invalid model response",
	)
)

// These classify every Failure an Interaction records without a Step error:
// on a failed Transition or on the failed settlement of one of its Effects.
// Either way the Failure becomes the Interaction's termination, so a Host
// matches it with failure.Classification() == FailureX. The Step errors above
// expose the same comparison through their Classification method.
var (
	// FailureToolCallsNotCompleted differs from ErrInvalidModelResponse: the
	// Step commits and consumes the model result, and none of its Tool calls
	// executes.
	FailureToolCallsNotCompleted = run.NewFailureClassification(run.FailureKindExternal, "interaction.model.tool_calls_not_completed")
	FailureModelFailed           = run.NewFailureClassification(run.FailureKindExternal, "interaction.model.failed")
	FailureModelNotExecuted      = run.NewFailureClassification(run.FailureKindExternal, "interaction.model.not_executed")
	// FailureContextRejected reports a ContextFailure from the ContextPreparer.
	FailureContextRejected = run.NewFailureClassification(run.FailureKindExternal, "interaction.context.rejected")
	// FailureDispatchRejected reports an Effect the Interaction dispatcher
	// cannot serve, such as one whose collaborator is not configured.
	FailureDispatchRejected          = run.NewFailureClassification(run.FailureKindContract, "interaction.dispatch.rejected")
	FailureToolAdvertisementInvalid  = run.NewFailureClassification(run.FailureKindContract, "interaction.tool.advertisement_invalid")
	FailureCompletionDecisionInvalid = run.NewFailureClassification(run.FailureKindContract, "interaction.completion.decision_invalid")
	FailureCompletionValidatorFailed = run.NewFailureClassification(run.FailureKindExecution, "interaction.completion.validator_failed")
	FailureContextPolicyFailed       = run.NewFailureClassification(run.FailureKindExecution, "interaction.context.policy_failed")
	FailureModelCallLimit            = run.NewFailureClassification(run.FailureKindExecution, "interaction.limit.model_calls")
	// FailureResponseByteLimit reports a final Output too large to deliver.
	FailureResponseByteLimit = run.NewFailureClassification(run.FailureKindExecution, "interaction.limit.response_bytes")
)
