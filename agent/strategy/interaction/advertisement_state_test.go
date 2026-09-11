package interaction

import (
	"context"
	"errors"
	"slices"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestAdvertisedToolNamesSurviveExecutionStateRestore(t *testing.T) {
	definition := advertisementTestDefinition(t)
	state := executionState{
		Phase: phaseReadyModel,
		WorkingContext: &chat.Request{Messages: []chat.Message{
			chat.NewUserMessage(chat.NewTextPart("restore deferred manifest")),
		}},
		ModelCallCount:      1,
		AdvertisedToolNames: []string{"first", "second"},
	}
	if validateErr := state.Validate(definition); validateErr != nil {
		t.Fatal(validateErr)
	}
	encoded, err := encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := definition.Restore(encoded)
	if err != nil {
		t.Fatal(err)
	}
	restoredExecution, ok := restored.(*execution)
	if !ok {
		t.Fatalf("restored Execution type = %T", restored)
	}
	if !slices.Equal(restoredExecution.state.AdvertisedToolNames, state.AdvertisedToolNames) {
		t.Fatalf(
			"restored advertised Tools = %v, want %v",
			restoredExecution.state.AdvertisedToolNames,
			state.AdvertisedToolNames,
		)
	}
	restoredExecution.state.AdvertisedToolNames[0] = "mutated"
	if state.AdvertisedToolNames[0] != "first" {
		t.Fatal("restored Execution aliases source state")
	}
}

func TestRestoreRejectsInvalidAdvertisements(t *testing.T) {
	definition := advertisementTestDefinition(t)
	for _, names := range [][]string{{"first", "first"}, {"unknown"}, {"initial"}, {" first"}, {""}} {
		state := executionState{Phase: phaseReadyModel,
			WorkingContext:      &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("restore"))}},
			AdvertisedToolNames: names,
		}
		encoded, err := encodeState(state)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := definition.Restore(encoded); !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatalf("Restore accepted advertisements %v: %v", names, err)
		}
	}
}

func advertisementTestDefinition(t testing.TB) *Definition {
	t.Helper()
	var executables []tool.Tool
	for _, name := range []string{"initial", "first", "second", "existing"} {
		executable, err := tool.NewFunc(tool.FuncConfig{Name: name, Description: "Exercise deferred tool recovery."},
			func(context.Context, struct{}) (string, error) { return "done", nil })
		if err != nil {
			t.Fatal(err)
		}
		executables = append(executables, executable)
	}
	toolSet, err := NewToolSet(ToolSetConfig{
		Name: "advertisement.tools", Description: "Exercise deferred tool recovery.",
		Tools: executables[:1], DeferredTools: executables[1:],
		ImplementationDigest: agent.ComputeDigest([]byte("advertisement-tools")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("advertisement-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewDefinition(DefinitionConfig{
		Name: "interaction.advertisement_restore", Description: "Verify deferred Tool manifest recovery.",
		MaxModelCalls: 2, Tools: toolSet, ToolBudget: agent.Budget{Steps: 10, Effects: 10, Signals: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}
