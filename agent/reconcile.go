package agent

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

func recoveryDefinition[I, O any](p run.Program[I, O], req run.EffectRequest) (*Definition, error) {
	if !req.Valid() || !p.Valid() || p.ProgramRef() != req.ProgramRef() {
		return nil, fmt.Errorf("%w: exact bound program and captured effect required", ErrInvalidProtocol)
	}
	d, ok := p.Definition().(*Definition)
	if !ok || !d.valid() {
		return nil, ErrInvalidDefinitionConfig
	}
	return d, nil
}

// SettleModelResult validates an investigated model response against the exact
// bound program and original effect. The effective input is already frozen.
func SettleModelResult[I, O any](p run.Program[I, O], req run.EffectRequest, response *chat.Response) (run.Settlement, error) {
	d, err := recoveryDefinition(p, req)
	if err != nil {
		return run.Settlement{}, err
	}
	env, err := decodeEffect(req.Effect().Payload())
	if err != nil {
		return run.Settlement{}, err
	}
	if env.operation() != operationModelCall || response == nil {
		return run.Settlement{}, ErrInvalidProtocol
	}
	return (modelCallResult{Response: response}).settlement(d.maxResponseBytes)
}

// SettlePreparedContext validates investigated messages for a context effect
// without repeating the processor or the model.
func SettlePreparedContext[I, O any](p run.Program[I, O], req run.EffectRequest, messages []chat.Message) (run.Settlement, error) {
	d, err := recoveryDefinition(p, req)
	if err != nil {
		return run.Settlement{}, err
	}
	return d.settlePreparedContext(req, messages)
}
