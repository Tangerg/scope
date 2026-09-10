package interaction

import (
	"context"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestDispatchersRejectNilContextBeforeProtocolValidation(t *testing.T) {
	definition, err := NewDefinition(DefinitionConfig{
		Name: "context.contract", Description: "Check the model dispatch context boundary.", MaxModelCalls: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := NewDispatcher(definition, DispatcherConfig{
		Client: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			t.Error("invalid context reached the model")
			return nil, errors.New("model must not run")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := tool.NewFunc(tool.FuncConfig{
		Name: "noop", Description: "Check the Tool dispatch context boundary.",
	}, func(context.Context, struct{}) (string, error) {
		t.Error("invalid context reached the Tool")
		return "", errors.New("Tool must not run")
	})
	if err != nil {
		t.Fatal(err)
	}
	toolSet, err := NewToolSet(ToolSetConfig{
		Name: "context.contract.tools", Description: "Check the Tool dispatch context boundary.",
		Tools:                []tool.Tool{executable},
		ImplementationDigest: agent.ComputeDigest([]byte("context-contract")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("context-contract-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, dispatcher := range map[string]agent.Dispatcher{"model": model, "tool": toolSet.dispatcher} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("nil context became a protocol result")
				}
			}()
			var nilContext context.Context
			_, _ = dispatcher.Dispatch(nilContext, agent.EffectRequest{}, nil)
		})
	}
}
