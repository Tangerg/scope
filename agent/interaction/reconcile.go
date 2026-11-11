package interaction

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func recoveryDefinition[I, O any](program run.Program[I, O], request run.EffectRequest) (*definition, error) {
	if !request.Valid() || !program.Valid() || program.ProgramRef() != request.ProgramRef() {
		return nil, fmt.Errorf("%w: exact bound program and captured effect required", ErrInvalidProtocol)
	}
	definition, bound := program.Definition().(*definition)
	if !bound {
		return nil, ErrInvalidConfig
	}
	return definition, nil
}

// SettleModelResult validates an investigated model response against the exact
// bound program and original effect. The effective input is already frozen.
func SettleModelResult[I, O any](program run.Program[I, O], request run.EffectRequest, response *chat.Response) (run.Settlement, error) {
	definition, err := recoveryDefinition(program, request)
	if err != nil {
		return run.Settlement{}, err
	}
	envelope, err := decodeEffect(request.Effect().Payload())
	if err != nil {
		return run.Settlement{}, err
	}
	if envelope.operation() != operationModelCall || response == nil {
		return run.Settlement{}, ErrInvalidProtocol
	}
	return definition.settleModelResponse(response, nil)
}

// SettlePreparedContext validates investigated messages for a context effect
// without repeating the processor or the model.
func SettlePreparedContext[I, O any](program run.Program[I, O], request run.EffectRequest, messages []chat.Message) (run.Settlement, error) {
	definition, err := recoveryDefinition(program, request)
	if err != nil {
		return run.Settlement{}, err
	}
	return definition.settlePreparedContext(request, messages)
}

// SettleToolResult takes advertised as the Tool identities its
// AdvertisingTool returned for this result, exactly as live dispatch reads them.
func SettleToolResult[I, O any](program run.Program[I, O], request run.EffectRequest, result tool.Result, disposition ResultDisposition, advertised []string) (run.Settlement, error) {
	definition, err := recoveryDefinition(program, request)
	if err != nil {
		return run.Settlement{}, err
	}
	if len(definition.tools.entries) == 0 {
		return run.Settlement{}, fmt.Errorf("%w: Tool recovery requires its interaction Program request", ErrInvalidProtocol)
	}
	manifest := definition.tools
	if !disposition.Valid() {
		return run.Settlement{}, fmt.Errorf("%w: Tool recovery disposition is invalid", ErrInvalidProtocol)
	}
	invocation, err := ToolInvocationFromRequest(request)
	if err != nil {
		return run.Settlement{}, err
	}
	call := invocation.ToolCall()
	entry, found := manifest.entries[call.Name]
	if !found {
		return run.Settlement{}, ErrInvalidProtocol
	}
	if _, prepareErr := entry.prepare(call, tool.Admission{}); prepareErr != nil {
		return run.Settlement{}, fmt.Errorf("%w: recovery input: %w", ErrInvalidProtocol, prepareErr)
	}
	if disposition == ResultSucceeded {
		err = entry.contract.ValidateResult(result)
	} else {
		err = result.Validate()
	}
	if err != nil {
		return run.Settlement{}, fmt.Errorf("%w: Tool recovery result: %w", ErrInvalidProtocol, err)
	}
	if result.InputRequest != nil {
		if disposition != ResultSucceeded || len(advertised) != 0 {
			return run.Settlement{}, fmt.Errorf("%w: an input request cannot fail or advertise Tools", ErrInvalidProtocol)
		}
		return (toolDispatchResult{InputRequest: result.InputRequest}).settlement()
	}
	canonical, err := manifest.advertisedAliases(advertised)
	if err != nil {
		return run.Settlement{}, fmt.Errorf("%w: Tool recovery advertisements: %w", ErrInvalidProtocol, err)
	}
	completion := &toolCallResult{Disposition: disposition, Output: result.Output.Clone(), AdvertisedToolNames: canonical}
	if completionErr := completion.validateCall(call); completionErr != nil {
		return run.Settlement{}, completionErr
	}
	return (toolDispatchResult{Completion: completion}).settlement()
}
